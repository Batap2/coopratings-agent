package main

// Notification-area (tray) icon. On Windows 11 it lands in the hidden icons
// by default (^ arrow next to the clock); it can be dragged onto the taskbar
// to keep it visible.
//
// Left click opens the app window (the login window when logged out); right
// click opens the menu: status, games to identify, recent activity,
// spreadsheet, start with Windows, diagnostic, logout, quit. A "!" badge is
// added when a game was not recognized by the server.
//
// Actions that change the state go through inbox\ (like other processes):
// only the agent loop touches the state.

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"fyne.io/systray"
	"github.com/ncruces/zenity"
)

const traySlots = 8

type trayUI struct {
	mu       sync.Mutex
	server   string
	dev      bool
	account  *systray.MenuItem
	connect  *systray.MenuItem
	playing  *systray.MenuItem
	header   *systray.MenuItem
	slots    [traySlots]*systray.MenuItem
	auto     *systray.MenuItem
	unknown  []unknownGame
	loggedIn bool
	badge    bool
	dialogOn bool
	loginOn  bool
}

var tray *trayUI

// runWithTray shows the tray icon and runs the agent in the background; it
// returns when the agent stops.
func runWithTray(a *agent, afterIcon func()) {
	systray.Run(func() {
		tray = newTrayUI(a.cfg, a.dev)
		if afterIcon != nil {
			go afterIcon()
		}
		go func() {
			a.run(false)
			systray.Quit()
		}()
	}, func() {})
}

func newTrayUI(cfg config, dev bool) *trayUI {
	t := &trayUI{server: strings.TrimRight(cfg.Server, "/"), dev: dev, loggedIn: cfg.token() != ""}
	systray.SetOnTapped(func() { go t.open() })
	systray.SetIcon(trayIconNormal)
	systray.SetTitle("CoopRatings")
	systray.SetTooltip("CoopRatings : suivi des jeux actif")

	t.account = systray.AddMenuItem("CoopRatings · "+orDefault(cfg.Username, "non connecté"), "")
	t.account.Disable()
	t.connect = systray.AddMenuItem("Se connecter…", "Vous connecter à CoopRatings pour suivre vos jeux")
	if cfg.token() != "" {
		t.connect.Hide()
	}
	t.playing = systray.AddMenuItem("", "")
	t.playing.Disable()
	t.playing.Hide()
	systray.AddSeparator()

	t.header = systray.AddMenuItem("Jeux à identifier :", "Jeux que CoopRatings n'a pas reconnus")
	t.header.Disable()
	t.header.Hide()
	for i := range t.slots {
		s := systray.AddMenuItem("", "Indiquer de quel jeu il s'agit")
		s.Hide()
		t.slots[i] = s
		go func(i int) {
			for range s.ClickedCh {
				go t.identify(i)
			}
		}(i)
	}

	activity := systray.AddMenuItem("Activité récente", "Les parties envoyées à CoopRatings, ou pas")
	open := systray.AddMenuItem("Ouvrir mon tableur", "")
	systray.AddSeparator()
	t.auto = systray.AddMenuItemCheckbox("Lancer au démarrage", "", autostartEnabled())
	if dev {
		t.auto.Disable()
	}
	diag := systray.AddMenuItem("Diagnostic (ce que l'appli détecte)", "")
	logout := systray.AddMenuItem("Se déconnecter", "")
	quit := systray.AddMenuItem("Quitter", "Arrêter l'appli jusqu'au prochain démarrage de Windows")

	go func() {
		for {
			select {
			case <-t.connect.ClickedCh:
				go t.login()
			case <-activity.ClickedCh:
				go showAppWindow(t.dev, "--activity")
			case <-open.ClickedCh:
				openURL(t.server + "/tableur?kind=game")
			case <-t.auto.ClickedCh:
				on := !autostartEnabled()
				if err := setAutostart(on); err == nil {
					// Persist the choice so the app does not re-register itself.
					cfg := loadConfig()
					cfg.AutostartOff = !on
					saveConfig(cfg)
					if on {
						t.auto.Check()
					} else {
						t.auto.Uncheck()
					}
				}
			case <-diag.ClickedCh:
				go runDiagnostic(true)
			case <-logout.ClickedCh:
				go t.logout()
			case <-quit.ClickedCh:
				postInbox(inboxMsg{Type: "quit"})
			}
		}
	}()
	return t
}

// update reflects the agent state (called by the loop after each tick).
func (t *trayUI) update(st *state, username string, loggedIn, offline bool) {
	if t == nil {
		return
	}
	var playing []string
	for _, s := range st.Active {
		playing = append(playing, s.Title)
	}
	sort.Strings(playing)
	unknown := make([]unknownGame, 0, len(st.Unknown))
	for _, u := range st.Unknown {
		unknown = append(unknown, u)
	}
	sort.Slice(unknown, func(i, j int) bool { return unknown[i].FirstSeen.Before(unknown[j].FirstSeen) })

	t.mu.Lock()
	defer t.mu.Unlock()
	t.unknown, t.loggedIn = unknown, loggedIn

	if loggedIn {
		t.account.SetTitle("CoopRatings · " + username)
		t.connect.Hide()
	} else {
		t.account.SetTitle("CoopRatings · non connecté")
		t.connect.Show()
	}
	tip := "CoopRatings : suivi des jeux actif"
	switch {
	case !loggedIn:
		tip = "CoopRatings : connectez-vous pour suivre vos jeux (clic sur l'icône)"
		t.playing.Hide()
	case len(playing) > 0:
		tip = "CoopRatings : en jeu sur " + strings.Join(playing, ", ")
		t.playing.SetTitle("En jeu : " + strings.Join(playing, ", "))
		t.playing.Show()
	default:
		t.playing.Hide()
	}
	if offline {
		tip += " (serveur injoignable)"
	}
	if len(unknown) > 0 {
		tip += fmt.Sprintf(" · %d jeu(x) à identifier", len(unknown))
	}
	systray.SetTooltip(tip)

	if len(unknown) > 0 {
		t.header.Show()
	} else {
		t.header.Hide()
	}
	for i, s := range t.slots {
		if i < len(unknown) {
			label := "⚠ « " + unknown[i].label() + " »"
			if unknown[i].Seconds > 0 {
				label += " · " + formatDuration(unknown[i].Seconds)
			}
			s.SetTitle(label)
			s.Show()
		} else {
			s.Hide()
		}
	}
	if badge := len(unknown) > 0 || !loggedIn; badge != t.badge {
		t.badge = badge
		if badge {
			systray.SetIcon(trayIconBadge)
		} else {
			systray.SetIcon(trayIconNormal)
		}
	}
}

// identify opens the "which game is this?" window (one at a time).
func (t *trayUI) identify(i int) {
	t.mu.Lock()
	if t.dialogOn || i >= len(t.unknown) {
		t.mu.Unlock()
		return
	}
	t.dialogOn = true
	u := t.unknown[i]
	t.mu.Unlock()
	identifyDialog(newAPI(loadConfig()), u)
	t.mu.Lock()
	t.dialogOn = false
	t.mu.Unlock()
}

func (t *trayUI) logout() {
	if zenity.Question("Se déconnecter de CoopRatings ? L'appli reste lancée : vous pourrez vous reconnecter depuis son icône.",
		zenity.Title(dlgTitle), zenity.OKLabel("Se déconnecter"), zenity.CancelLabel("Annuler")) != nil {
		return
	}
	cfg := loadConfig()
	cfg.Username = ""
	cfg.setToken("")
	saveConfig(cfg)
	postInbox(inboxMsg{Type: "logout"})
}

// firstLogin runs the first login, right after the icon appears.
func (t *trayUI) firstLogin() { t.doLogin(true) }

// login opens the login window (one at a time). It is a no-op on a nil
// trayUI (--once mode, no icon).
func (t *trayUI) login() { t.doLogin(false) }

// open handles a left click on the icon.
func (t *trayUI) open() {
	t.mu.Lock()
	loggedIn := t.loggedIn
	t.mu.Unlock()
	if !loggedIn {
		t.login()
		return
	}
	showAppWindow(t.dev)
}

func (t *trayUI) doLogin(welcome bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	if t.loginOn {
		t.mu.Unlock()
		return
	}
	t.loginOn = true
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		t.loginOn = false
		t.mu.Unlock()
	}()
	cfg := loadConfig()
	// After a first login the window itself shows the welcome screen.
	if !loginWindow(&cfg, welcome) {
		return // cancelled: the tray menu still offers to log in
	}
	postInbox(inboxMsg{Type: "reload"})
	if !welcome {
		showToast("Connecté à CoopRatings", "Bonjour "+cfg.Username+" ! Vos jeux sont maintenant suivis.", "")
	}
}

// loadUnknown reads the games to identify from the state (control window).
func loadUnknown() []unknownGame {
	st := loadState()
	list := make([]unknownGame, 0, len(st.Unknown))
	for _, u := range st.Unknown {
		list = append(list, u)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].FirstSeen.Before(list[j].FirstSeen) })
	return list
}
