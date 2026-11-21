// CoopRatings Agent: a small Windows program that detects the games being
// played and reports them to CoopRatings (see README.md).
//
// Invocations:
//
//	(no argument)              First run: install + log in, then track in the
//	                           background with a notification-area icon.
//	                           Already running: open the control window.
//	--autostart                launched at Windows startup (silent)
//	--activity                 already running: open the recent activity
//	coopratings-agent:…        notification click/buttons (rate, mute, login)
//	--diagnostic               detection report in a text file
//	--test-notification        show a test notification
//
// Development options: --dev (separate folder, no installation),
// --server URL, --login user:password, --interval 10s, --min-session 30s, --once.
package main

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ncruces/zenity"
	"golang.org/x/sys/windows"
)

var version = "dev" // set at build time: -ldflags "-X main.version=1.0.0"

func main() {
	// Preview the windows with sample data (development).
	if len(os.Args) == 3 && os.Args[1] == "--preview-ui" {
		initPaths(true)
		previewUI(os.Args[2])
		return
	}
	// Build helper: writes the exe icon (see gen.go).
	if len(os.Args) == 3 && os.Args[1] == "--write-icon" {
		if err := os.WriteFile(os.Args[2], appICO(), 0o644); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		return
	}
	var (
		diag, dev, once, autostart, testNotif bool
		afterUpdate, activity                 bool
		uninstallReq, quiet                   bool
		server, devLogin, protoArg            string
		interval                              = time.Minute
	)
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--diagnostic":
			diag = true
		case a == "--test-notification":
			testNotif = true
		case a == "--dev":
			dev = true
		case a == "--once":
			once = true
		case a == "--autostart":
			autostart = true
		case a == "--activity":
			activity = true
		case a == uninstallFlag:
			// "Uninstall" button of Windows "Installed apps" (install.go).
			uninstallReq = true
		case a == quietFlag:
			quiet = true
		case a == afterUpdateFlag:
			// Relaunched by the auto-updater (see update.go).
			afterUpdate, autostart = true, true
		case a == "--server" && i+1 < len(args):
			server = args[i+1]
			i++
		case a == "--login" && i+1 < len(args):
			devLogin = args[i+1]
			i++
		case a == "--min-session" && i+1 < len(args):
			if d, err := time.ParseDuration(args[i+1]); err == nil {
				minSessionSeconds = int(d.Seconds())
			}
			i++
		case a == "--interval" && i+1 < len(args):
			if d, err := time.ParseDuration(args[i+1]); err == nil && d >= time.Second {
				interval = d
			}
			i++
		case strings.HasPrefix(strings.ToLower(a), protocolScheme+":"):
			protoArg = a
		}
	}

	if err := initPaths(dev); err != nil {
		zenity.Error("CoopRatings Agent ne peut pas démarrer : "+err.Error(), zenity.Title(dlgTitle))
		return
	}
	setupLog()

	switch {
	case uninstallReq:
		uninstallFromWindows(quiet)
		return
	case protoArg != "":
		handleProtocol(protoArg)
		return
	case diag:
		fmt.Println(runDiagnostic(!dev))
		return
	case testNotif:
		cfg := loadConfig()
		registerShellIntegration(currentExe())
		err := showToast("Test de CoopRatings Agent", "Les notifications fonctionnent.", strings.TrimRight(cfg.Server, "/")+"/appli",
			toastAction{Label: "Ouvrir le site", URL: strings.TrimRight(cfg.Server, "/")})
		fmt.Println("notification :", err)
		return
	}

	cfg := loadConfig()
	if server != "" {
		cfg.Server = server
		saveConfig(cfg)
	}

	// Already running: show the login window if logged out, otherwise the
	// control window.
	mutex, ok := acquireMutex(mutexName)
	// After an update, wait for the old version to finish exiting.
	for deadline := time.Now().Add(3 * time.Minute); !ok && afterUpdate && time.Now().Before(deadline); {
		time.Sleep(500 * time.Millisecond)
		mutex, ok = acquireMutex(mutexName)
	}
	if !ok {
		if !autostart {
			if cfg.token() == "" {
				if askLogin(&cfg) == nil {
					postInbox(inboxMsg{Type: "reload"})
				}
			} else if m, ok := acquireMutex(mutexName + "-window"); !ok {
				focusWindow(appWindowTitle) // already open (double click on the icon…)
			} else {
				defer windows.CloseHandle(m)
				if activity {
					controlPanel(cfg, dev, "activity")
				} else {
					controlPanel(cfg, dev, "control")
				}
			}
		}
		return
	}

	// First run from the downloaded file: copy it to
	// %LOCALAPPDATA%\CoopRatingsAgent, then relaunch that copy.
	if !dev && !isInstalledCopy() {
		windows.CloseHandle(mutex)
		// A newer version is already installed (auto-update): launch it
		// instead of downgrading.
		var st status
		readJSON(appFile("status.json"), &st)
		if _, err := os.Stat(installedExe()); err == nil && isNewer(st.Version, version) {
			if err := relaunch(nil); err != nil {
				zenity.Error("Lancement impossible : "+err.Error(), zenity.Title(dlgTitle))
			}
			return
		}
		if err := installSelf(); err != nil {
			zenity.Error("Installation impossible : "+err.Error(), zenity.Title(dlgTitle))
			return
		}
		if err := relaunch(nil); err != nil {
			zenity.Error("Lancement impossible : "+err.Error(), zenity.Title(dlgTitle))
		}
		return
	}
	defer windows.CloseHandle(mutex)
	if !dev {
		cleanupUpdate()
	}
	if afterUpdate {
		log.Printf("mis à jour : version %s", version)
	}

	// Register Windows integration and autostart at install time, BEFORE any
	// login: the app must start with Windows even while logged out, so it can
	// remind the user to log in. Only an explicit opt-out (unchecking
	// autostart in the tray menu) disables it.
	exe := installedExe()
	if dev {
		exe = currentExe()
	}
	if err := registerShellIntegration(exe); err != nil {
		log.Printf("intégration Windows : %v", err)
	}
	if !dev {
		if err := registerUninstallEntry(exe, cfg.Server); err != nil {
			log.Printf("applications installées : %v", err)
		}
	}
	if !dev && !cfg.AutostartOff && !autostartEnabled() {
		if err := setAutostart(true); err != nil {
			log.Printf("démarrage automatique : %v", err)
		}
	}

	// Logged out, whether launched manually or with Windows: the login window
	// opens AFTER the tray icon appears (otherwise the app runs without an
	// icon while the window is open). Cancelling does not stop the app: it
	// waits in the tray, whose icon and menu offer to log in.
	askNow := false
	if cfg.token() == "" {
		if devLogin != "" {
			user, pass, _ := strings.Cut(devLogin, ":")
			token, username, err := newAPI(cfg).login(user, pass)
			if err != nil {
				log.Printf("connexion : %v", err)
				fmt.Println("connexion impossible :", err)
				return
			}
			cfg.Username = username
			cfg.setToken(token)
			saveConfig(cfg)
		} else {
			askNow = true
		}
	}
	if !askNow && !autostart && !dev {
		showToast("CoopRatings Agent est lancé", "Il suit vos jeux en arrière-plan.", "")
	}

	a := newAgent(cfg, interval, dev)
	if once {
		a.run(true)
		return
	}
	if askNow {
		runWithTray(a, func() { tray.firstLogin() })
		return
	}
	runWithTray(a, nil)
}

// remindLogin shows a "log in" notification when no login window can be
// opened (--once mode, without a tray icon). Clicking it opens the login
// window.
func remindLogin() {
	showToast("CoopRatings : connectez-vous", "Cliquez ici pour vous connecter et suivre vos jeux automatiquement.",
		protocolScheme+":login")
}

// handleProtocol handles a notification button, e.g.
// coopratings-agent:mute?item=123 ("don't remind me for this game").
func handleProtocol(raw string) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return
	}
	action := strings.Trim(strings.ToLower(u.Opaque+u.Host+u.Path), "/")
	switch action {
	case "login":
		// Click on the "log in" notification: the main process opens its
		// login window.
		if mutexExists(mutexName) {
			postInbox(inboxMsg{Type: "showlogin"})
		}
	case "rate":
		// "Rate" button (or click on the notification): small grade + status
		// window, one at a time.
		id, err := strconv.Atoi(u.Query().Get("item"))
		if err != nil || id <= 0 {
			return
		}
		m, ok := acquireMutex(mutexName + "-rate")
		if !ok {
			return
		}
		defer windows.CloseHandle(m)
		secs, _ := strconv.Atoi(u.Query().Get("s"))
		rateWindow(loadConfig(), id, secs)
	case "mute":
		id, err := strconv.Atoi(u.Query().Get("item"))
		if err != nil || id <= 0 {
			return
		}
		// The main process mutes the reminder and tells the server, retrying
		// later if it is unreachable.
		postInbox(inboxMsg{Type: "mute", ItemID: id})
		if !mutexExists(mutexName) {
			newAPI(loadConfig()).mute(id)
		}
		log.Printf("rappels coupés pour le jeu %d (depuis une notification)", id)
	}
}
