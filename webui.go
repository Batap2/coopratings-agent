package main

// App windows styled like the website (login, welcome, unrecognized game, rate
// a game, control window), rendered with the Windows web engine (WebView2,
// built into Windows 11 and most Windows 10 installs).
//
// The page is ui/app.html (site styles, embedded Rubik font). It calls Go via
// window.__go(id, method, argsJSON); the reply comes back through
// window.__resolve. Network calls run off the window thread so the window
// does not freeze during a request.
//
// Without WebView2, each window falls back to its classic Windows dialog
// (ui.go).

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"github.com/ncruces/zenity"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

//go:embed ui/app.html ui/rubik-var.woff2
var uiFiles embed.FS

var (
	ole32                   = windows.NewLazySystemDLL("ole32.dll")
	procCoInitializeEx      = ole32.NewProc("CoInitializeEx")
	procGetDpiForSystem     = user32.NewProc("GetDpiForSystem")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
)

type uiHandler func(method string, args []json.RawMessage) (any, error)

func buildUIHTML(view string, data any) string {
	page, _ := uiFiles.ReadFile("ui/app.html")
	font, _ := uiFiles.ReadFile("ui/rubik-var.woff2")
	app, _ := json.Marshal(map[string]any{ // < > & are escaped by json
		"view": view, "data": data,
		"taskbarLight": windowsLightTaskbar(),
	})
	face := "@font-face{font-family:'Rubik';font-weight:300 900;font-display:block;src:url(data:font/woff2;base64," +
		base64.StdEncoding.EncodeToString(font) + ") format('woff2');}"
	html := strings.Replace(string(page), "/*FONT*/", face, 1)
	return strings.Replace(html, "/*APP*/", "window.APP = "+string(app)+";", 1)
}

// themeTitleBar matches the title bar to the window: dark when Windows is in
// dark mode and, on Windows 11, the site's exact colors.
func themeTitleBar(hwnd windows.HWND) {
	dark := windowsDarkMode()
	var on int32
	if dark {
		on = 1
	}
	windows.DwmSetWindowAttribute(hwnd, 20, unsafe.Pointer(&on), 4) // DWMWA_USE_IMMERSIVE_DARK_MODE
	caption, text := uint32(0x00DEECF1), uint32(0x00423607)         // #f1ecde / #073642 (COLORREF = 0x00BBGGRR)
	if dark {
		caption, text = 0x0020252A, 0x00B9CDD6 // #2a2520 / #d6cdb9 (Espresso)
	}
	windows.DwmSetWindowAttribute(hwnd, 35, unsafe.Pointer(&caption), 4) // DWMWA_CAPTION_COLOR
	windows.DwmSetWindowAttribute(hwnd, 34, unsafe.Pointer(&caption), 4) // DWMWA_BORDER_COLOR
	windows.DwmSetWindowAttribute(hwnd, 36, unsafe.Pointer(&text), 4)    // DWMWA_TEXT_COLOR
}

// The app theme (windows) and the "Windows" theme (taskbar) are two separate
// settings in Settings > Colors.
func windowsDarkMode() bool { return personalizeValue("AppsUseLightTheme") == 0 }

func windowsLightTaskbar() bool { return personalizeValue("SystemUsesLightTheme") == 1 }

// personalizeValue reads a theme setting; -1 if absent.
func personalizeValue(name string) int {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return -1
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue(name)
	if err != nil {
		return -1
	}
	return int(v)
}

func dpiScale() float64 {
	if procGetDpiForSystem.Find() != nil {
		return 1
	}
	dpi, _, _ := procGetDpiForSystem.Call()
	if dpi == 0 {
		return 1
	}
	return float64(dpi) / 96
}

// showWebWindow opens a window and waits until it is closed. It returns
// false if WebView2 is not available.
func showWebWindow(title, view string, data any, width, height int, handle uiHandler) bool {
	return showWebWindowAt(title, view, data, width, height, false, handle)
}

// showWebWindowAt is showWebWindow with the window either centered or, with
// corner, in the bottom-right corner of the screen.
func showWebWindowAt(title, view string, data any, width, height int, corner bool, handle uiHandler) bool {
	result := make(chan bool, 1)
	go func() {
		// Dedicated OS thread for the window (COM + message loop). It is
		// never unlocked: Go destroys it, COM included, when the goroutine ends.
		runtime.LockOSThread()
		procCoInitializeEx.Call(0, 2) // COINIT_APARTMENTTHREADED
		scale := dpiScale()
		pw, ph := int(float64(width)*scale), int(float64(height)*scale)
		w := webview2.NewWithOptions(webview2.WebViewOptions{
			DataPath:      appFile("webview2"),
			AutoFocus:     true,
			WindowOptions: webview2.WindowOptions{Title: title, Width: uint(pw), Height: uint(ph), Center: !corner, IconId: 32512}, // 32512 = IDI_APPLICATION (see gen.go)
		})
		if w == nil {
			result <- false
			return
		}
		w.SetSize(pw, ph, webview2.HintFixed)
		if corner {
			moveToCorner(windows.HWND(uintptr(w.Window())), int32(12*scale))
		}
		themeTitleBar(windows.HWND(uintptr(w.Window())))
		w.Bind("__go", func(id int, method string, argsJSON string) {
			go func() {
				var args []json.RawMessage
				json.Unmarshal([]byte(argsJSON), &args)
				var res any
				var err error
				switch method {
				case "close":
					w.Dispatch(func() { w.Terminate() })
					return
				case "openURL":
					var u string
					if len(args) > 0 {
						json.Unmarshal(args[0], &u)
					}
					if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
						openURL(u)
					}
					res = true
				default:
					res, err = handle(method, args)
				}
				var payload []byte
				if err == nil {
					payload, _ = json.Marshal(res)
				} else {
					payload, _ = json.Marshal(err.Error())
				}
				ok := err == nil
				w.Dispatch(func() { w.Eval(fmt.Sprintf("window.__resolve(%d,%v,%s)", id, ok, payload)) })
			}()
		})
		w.SetHtml(buildUIHTML(view, data))
		procSetForegroundWindow.Call(uintptr(w.Window()))
		w.Run()
		w.Destroy()
		result <- true
	}()
	return <-result
}

func argString(args []json.RawMessage, i int) string {
	var s string
	if i < len(args) {
		json.Unmarshal(args[i], &s)
	}
	return s
}

func argInt(args []json.RawMessage, i int) int {
	var n int
	if i < len(args) {
		json.Unmarshal(args[i], &n)
	}
	return n
}

// friendlyError turns a network error into a readable message.
func friendlyError(err error, server string) error {
	m := err.Error()
	if strings.Contains(m, "dial ") || strings.Contains(m, "connectex") || strings.Contains(m, "no such host") ||
		strings.Contains(m, "Client.Timeout") || strings.Contains(m, "connection refused") {
		return fmt.Errorf("Impossible de joindre CoopRatings (%s). Vérifiez votre connexion à Internet.",
			strings.TrimPrefix(strings.TrimPrefix(server, "https://"), "http://"))
	}
	if len(m) > 0 {
		m = strings.ToUpper(m[:1]) + m[1:]
	}
	return errors.New(m + ".")
}

// --- Login -------------------------------------------------------------------

// loginWindow shows the login window; with welcome, it then shows the
// welcome screen after a first login. It returns true on successful login.
func loginWindow(cfg *config, welcome bool) bool {
	var connected atomic.Bool
	server := strings.TrimRight(cfg.Server, "/")
	shown := showWebWindow("CoopRatings : connexion", "login",
		map[string]any{"server": server, "username": cfg.Username, "welcome": welcome}, 440, 610,
		func(method string, args []json.RawMessage) (any, error) {
			if method != "login" {
				return nil, errors.New("action inconnue")
			}
			api := &apiClient{server: server, http: newAPI(*cfg).http}
			token, username, err := api.login(argString(args, 0), argString(args, 1))
			if err != nil {
				return nil, friendlyError(err, server)
			}
			cfg.Username = username
			if err := cfg.setToken(token); err != nil {
				return nil, err
			}
			if err := saveConfig(*cfg); err != nil {
				return nil, err
			}
			connected.Store(true)
			postInbox(inboxMsg{Type: "reload"}) // applied without waiting for the window to close
			return map[string]string{"username": username}, nil
		})
	if shown {
		return connected.Load()
	}
	// Without WebView2: classic Windows dialogs.
	if askLoginClassic(cfg) != nil {
		return false
	}
	if welcome {
		zenity.Info(fmt.Sprintf("C'est prêt, %s !\n\nCoopRatings Agent suit maintenant vos jeux en arrière-plan "+
			"et se lancera avec Windows.\n\nSon icône est dans les icônes cachées, à côté de l'horloge.", cfg.Username),
			zenity.Title(dlgTitle))
	}
	return true
}

// askLogin shows the login window without the welcome screen.
func askLogin(cfg *config) error {
	if loginWindow(cfg, false) {
		return nil
	}
	return zenity.ErrCanceled
}

// --- Unrecognized game -------------------------------------------------------

func identifyDialog(api *apiClient, u unknownGame) {
	shown := showWebWindow("CoopRatings : quel est ce jeu ?", "identify",
		map[string]any{"name": u.Name, "label": u.label(), "path": u.Path, "seconds": u.Seconds}, 540, 640,
		func(method string, args []json.RawMessage) (any, error) {
			switch method {
			case "search":
				hits, err := api.search(argString(args, 0))
				if err != nil {
					return nil, friendlyError(err, api.server)
				}
				if hits == nil {
					hits = []apiSearchHit{}
				}
				return hits, nil
			case "resolve":
				itemID, title, err := api.resolve(argInt(args, 0), argInt(args, 1), argString(args, 2))
				if err != nil {
					return nil, friendlyError(err, api.server)
				}
				if itemID > 0 {
					postInbox(inboxMsg{Type: "resolve", Key: u.Key, ItemID: itemID, Title: title})
				}
				return true, nil
			case "notGame":
				postInbox(inboxMsg{Type: "notgame", Key: u.Key})
				return true, nil
			}
			return nil, errors.New("action inconnue")
		})
	if !shown {
		identifyDialogClassic(api, u)
	}
}

// --- Rate a game (button of the end-of-session notification) -----------------

// rateWindow sets a grade (out of 20) and status without going through the
// browser. If the server does not respond, it opens the game's row in the
// spreadsheet instead.
func rateWindow(cfg config, itemID, seconds int) {
	api := newAPI(cfg)
	fallback := fmt.Sprintf("%s/tableur?jeu=%d", api.server, itemID)
	g, err := api.game(itemID)
	if err != nil {
		log.Printf("noter le jeu %d : %v", itemID, err)
		openURL(fallback)
		return
	}
	if g.URL != "" {
		fallback = g.URL
	}
	shown := showWebWindow("CoopRatings : noter "+g.Title, "rate",
		map[string]any{"game": g, "seconds": seconds}, 460, 520,
		func(method string, args []json.RawMessage) (any, error) {
			if method != "rate" || len(args) < 2 {
				return nil, errors.New("action inconnue")
			}
			var grade, status *int
			json.Unmarshal(args[0], &grade)
			json.Unmarshal(args[1], &status)
			saved, err := api.rate(itemID, grade, status)
			if err != nil {
				return nil, friendlyError(err, api.server)
			}
			log.Printf("noté depuis la notification : %s", saved.Title)
			return saved, nil
		})
	if !shown {
		openURL(fallback)
	}
}

// --- Control window (shown when the app is launched while running) -----------

const appWindowTitle = "CoopRatings Agent"

func orInt(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

// controlPanel shows the control window, opened on view "control" or
// "activity" (recent sessions).
func controlPanel(cfg config, dev bool, view string) {
	var st status
	readJSON(appFile("status.json"), &st)
	playing := st.Playing
	if playing == nil {
		playing = []string{}
	}
	recent := st.Recent
	if recent == nil {
		recent = []recentEntry{}
	}
	data := map[string]any{
		"version": version, "username": orDefault(cfg.Username, "?"), "playing": playing,
		"queue": st.Queue, "offline": st.Offline, "unknown": st.Unknown,
		"recent": recent, "minSession": orInt(st.MinSession, minSessionSeconds),
		// Downloaded exe newer than the running version: offer to install it.
		"canUpdate": !dev && !isInstalledCopy() && isNewer(version, st.Version),
	}
	server := strings.TrimRight(cfg.Server, "/")
	var after atomic.Value // action to run once the window is closed
	after.Store("")
	shown := showWebWindowAt(appWindowTitle, view, data, 460, 600, true,
		func(method string, args []json.RawMessage) (any, error) {
			if method != "action" {
				return nil, errors.New("action inconnue")
			}
			switch a := argString(args, 0); a {
			case "tableur":
				openURL(strings.TrimRight(cfg.Server, "/") + "/tableur?kind=game")
			case "logout":
				c := loadConfig()
				c.Username = ""
				c.setToken("")
				saveConfig(c)
				postInbox(inboxMsg{Type: "logout"})
			case "quit":
				postInbox(inboxMsg{Type: "quit"})
			case "identify", "update", "uninstall":
				after.Store(a)
			default:
				if id, err := strconv.Atoi(strings.TrimPrefix(a, "game:")); strings.HasPrefix(a, "game:") && err == nil && id > 0 {
					openURL(server + "/tableur?jeu=" + strconv.Itoa(id))
				}
			}
			return true, nil
		})
	if !shown {
		controlPanelClassic(cfg, dev)
		return
	}
	switch after.Load().(string) {
	case "identify":
		api := newAPI(cfg)
		for _, u := range loadUnknown() {
			identifyDialog(api, u)
		}
	case "update":
		postInbox(inboxMsg{Type: "quit"})
		if waitMainExit(10*time.Second) && installSelf() == nil && relaunch(nil) == nil {
			showToast("CoopRatings Agent mis à jour", "La nouvelle version est lancée.", "")
		} else {
			zenity.Error("La mise à jour a échoué. Réessayez dans un instant.", zenity.Title(dlgTitle))
		}
	case "uninstall":
		postInbox(inboxMsg{Type: "quit"})
		waitMainExit(10 * time.Second)
		showToast("CoopRatings Agent est désinstallé", "Merci de l'avoir essayé !", "")
		uninstall()
	}
}

// previewUI opens a window with sample data (--preview-ui).
func previewUI(view string) {
	slow := func() { time.Sleep(500 * time.Millisecond) }
	switch view {
	case "login", "welcome":
		showWebWindow("CoopRatings : connexion", view,
			map[string]any{"server": "https://coopratings.duckdns.org", "username": "Batap", "welcome": true}, 440, 610,
			func(method string, args []json.RawMessage) (any, error) {
				slow()
				if argString(args, 1) == "demo" {
					return map[string]string{"username": argString(args, 0)}, nil
				}
				return nil, errors.New("Identifiant ou mot de passe incorrect.")
			})
	case "identify":
		showWebWindow("CoopRatings : quel est ce jeu ?", "identify",
			map[string]any{"name": "Hades II", "label": "Hades II", "path": `C:\Users\moi\Desktop\Hades.II.v1.0-GOG\Hades2.exe`, "seconds": 2460}, 540, 640,
			func(method string, args []json.RawMessage) (any, error) {
				slow()
				return []apiSearchHit{
					{ItemID: 825, Title: "Hades II", Year: 2025, Detail: "sur CoopRatings", Type: "Jeu principal",
						Platforms: []string{"Mac", "Switch", "Switch 2", "PC", "PS5", "Series X|S"}},
					{ItemID: 659, Title: "Hades", Year: 2020, Detail: "sur CoopRatings", Type: "Jeu principal",
						Platforms: []string{"Mac", "Switch", "PC", "PS4", "PS5", "XONE", "Series X|S", "iOS"}},
					{IGDBID: 2, Title: "Hades II: Soundtrack Pack", Year: 2025, Detail: "IGDB", Type: "DLC", Platforms: []string{"PC"}},
				}, nil
			})
	case "rate":
		grade, status := 14, 0
		showWebWindow("CoopRatings : noter Dice Hoarder", "rate",
			map[string]any{"game": apiGame{ItemID: 1, Title: "Dice Hoarder", Year: 2025, Status: &status,
				URL: "https://coopratings.duckdns.org/tableur?jeu=1"}, "seconds": 14520}, 460, 520,
			func(method string, args []json.RawMessage) (any, error) {
				slow()
				return apiGame{ItemID: 1, Title: "Dice Hoarder", Grade: &grade, Status: &status}, nil
			})
	case "control", "activity":
		now := time.Now()
		ago := func(h float64) time.Time { return now.Add(-time.Duration(h * float64(time.Hour))) }
		recent := []recentEntry{
			{Title: "Hades II", End: ago(0.3), Seconds: 2460, State: "identify"},
			{Title: "Dice Hoarder", ItemID: 1, End: ago(2), Seconds: 4820, State: "hours"},
			{Title: "Balatro", ItemID: 2, End: ago(5), Seconds: 180, State: "short"},
			{Title: "The Entropy Centre", ItemID: 3, End: ago(26), Seconds: 7300, State: "added"},
			{Title: "Wallpaper Engine", End: ago(27), Seconds: 3600, State: "notgame"},
			{Title: "Duskers", ItemID: 4, End: ago(75), Seconds: 1500, State: "removed"},
		}
		showWebWindowAt(appWindowTitle, view,
			map[string]any{"version": version, "username": "Batap", "playing": []string{"Dice Hoarder"},
				"queue": 1, "offline": false, "unknown": 1, "canUpdate": false,
				"recent": recent, "minSession": minSessionSeconds}, 460, 600, true,
			func(method string, args []json.RawMessage) (any, error) { return true, nil })
	}
}
