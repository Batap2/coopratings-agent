package main

// Classic Windows dialogs (via zenity), used when WebView2 is unavailable:
// login, control window, identification of an unknown game.

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ncruces/zenity"
)

const dlgTitle = "CoopRatings Agent"

// askLoginClassic asks for login + password until success or cancellation.
func askLoginClassic(cfg *config) error {
	msg := ""
	for {
		opts := []zenity.Option{zenity.Title(dlgTitle + " : connexion"), zenity.Username(), zenity.OKLabel("Se connecter"), zenity.CancelLabel("Annuler")}
		if msg != "" {
			zenity.Error(msg, zenity.Title(dlgTitle))
		}
		login, pass, err := zenity.Password(opts...)
		if err != nil {
			return err // cancelled
		}
		if strings.TrimSpace(login) == "" || pass == "" {
			msg = "Indiquez votre identifiant (pseudo ou e-mail) et votre mot de passe CoopRatings."
			continue
		}
		token, username, err := (&apiClient{server: strings.TrimRight(cfg.Server, "/"), http: newAPI(*cfg).http}).login(login, pass)
		if err != nil {
			msg = "Connexion impossible : " + err.Error()
			continue
		}
		cfg.Username = username
		if err := cfg.setToken(token); err != nil {
			return err
		}
		return saveConfig(*cfg)
	}
}

// controlPanelClassic is the window shown when the app is launched while
// already running.
func controlPanelClassic(cfg config, dev bool) {
	var st status
	readJSON(appFile("status.json"), &st)
	var info []string
	info = append(info, fmt.Sprintf("Connecté en tant que %s.", orDefault(cfg.Username, "?")))
	switch {
	case len(st.Playing) > 0:
		info = append(info, "En train de jouer : "+strings.Join(st.Playing, ", ")+".")
	default:
		info = append(info, "Suivi actif : aucun jeu en cours.")
	}
	if st.Queue > 0 {
		info = append(info, fmt.Sprintf("%d session(s) en attente d'envoi.", st.Queue))
	}
	if st.Offline {
		info = append(info, "Serveur injoignable pour le moment.")
	}
	if st.Unknown > 0 {
		info = append(info, fmt.Sprintf("%d jeu(x) à identifier.", st.Unknown))
	}
	if len(st.Recent) > 0 {
		info = append(info, "", "Activité récente :")
		for i, r := range st.Recent {
			if i == 5 {
				break
			}
			info = append(info, fmt.Sprintf("• %s (%s) : %s", r.Title, formatDuration(r.Seconds), recentLabel(r, st.MinSession)))
		}
	}

	const (
		optTableur  = "Ouvrir mon tableur"
		optRelogin  = "Se reconnecter"
		optLogout   = "Se déconnecter"
		optQuit     = "Arrêter l'appli (jusqu'au prochain démarrage de Windows)"
		optUpdate   = "Installer cette version (mise à jour)"
		optUninstal = "Désinstaller"
		optUnknown  = "Identifier les jeux non reconnus"
	)
	items := []string{optTableur}
	if st.Unknown > 0 {
		items = append(items, optUnknown)
	}
	items = append(items, optRelogin, optLogout, optQuit)
	if !dev && !isInstalledCopy() {
		items = append(items, optUpdate)
	}
	items = append(items, optUninstal)

	choice, err := zenity.List(strings.Join(info, "\n"), items,
		zenity.Title(dlgTitle+" "+version), zenity.OKLabel("Valider"), zenity.CancelLabel("Fermer"),
		zenity.Width(460), zenity.Height(480), zenity.DisallowEmpty())
	if err != nil {
		return
	}
	switch choice {
	case optTableur:
		u := strings.TrimRight(cfg.Server, "/") + "/tableur?kind=game"
		openURL(u)
	case optUnknown:
		api := newAPI(cfg)
		for _, u := range loadUnknown() {
			identifyDialog(api, u)
		}
	case optRelogin:
		if askLogin(&cfg) == nil {
			postInbox(inboxMsg{Type: "reload"})
			zenity.Info("Reconnecté en tant que "+cfg.Username+".", zenity.Title(dlgTitle))
		}
	case optLogout:
		cfg.Username = ""
		cfg.setToken("")
		saveConfig(cfg)
		postInbox(inboxMsg{Type: "logout"})
		zenity.Info("Déconnecté. L'appli reste lancée : reconnectez-vous depuis son icône (« Se connecter… »).", zenity.Title(dlgTitle))
	case optQuit:
		postInbox(inboxMsg{Type: "quit"})
	case optUpdate:
		postInbox(inboxMsg{Type: "quit"})
		if waitMainExit(10*time.Second) && installSelf() == nil && relaunch(nil) == nil {
			zenity.Info("Mise à jour installée.", zenity.Title(dlgTitle))
		} else {
			zenity.Error("La mise à jour a échoué. Réessayez dans un instant.", zenity.Title(dlgTitle))
		}
	case optUninstal:
		if zenity.Question("Désinstaller CoopRatings Agent ? Les sessions pas encore envoyées seront perdues.",
			zenity.Title(dlgTitle), zenity.OKLabel("Désinstaller"), zenity.CancelLabel("Annuler")) != nil {
			return
		}
		postInbox(inboxMsg{Type: "quit"})
		waitMainExit(10 * time.Second)
		uninstall()
		zenity.Info("CoopRatings Agent est désinstallé.", zenity.Title(dlgTitle))
	}
}

func waitMainExit(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !mutexExists(mutexName) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// identifyDialogClassic lets the user identify an unknown game manually.
func identifyDialogClassic(api *apiClient, u unknownGame) {
	const (
		optName    = "Indiquer le jeu…"
		optNotGame = "Ce n'est pas un jeu (ne plus le suivre)"
	)
	text := fmt.Sprintf("CoopRatings n'a pas reconnu ce jeu :\n%s\n%s", u.Name, u.Path)
	if u.Seconds > 0 {
		text += fmt.Sprintf("\n(%s de jeu en attente)", formatDuration(u.Seconds))
	}
	choice, err := zenity.List(text, []string{optName, optNotGame},
		zenity.Title(dlgTitle), zenity.OKLabel("Valider"), zenity.CancelLabel("Plus tard"),
		zenity.Width(520), zenity.Height(300), zenity.DisallowEmpty())
	if err != nil {
		return
	}
	if choice == optNotGame {
		postInbox(inboxMsg{Type: "notgame", Key: u.Key})
		return
	}
	query := u.Name
	for {
		q, err := zenity.Entry("Nom du jeu :", zenity.Title(dlgTitle), zenity.EntryText(query),
			zenity.OKLabel("Rechercher"), zenity.CancelLabel("Annuler"))
		if err != nil {
			return
		}
		query = strings.TrimSpace(q)
		if query == "" {
			continue
		}
		hits, err := api.search(query)
		if err != nil {
			zenity.Error("Recherche impossible : "+err.Error(), zenity.Title(dlgTitle))
			return
		}
		labels := make([]string, 0, len(hits)+2)
		for _, h := range hits {
			l := h.Title
			if h.Year > 0 {
				l += fmt.Sprintf(" (%d)", h.Year)
			}
			if h.Type != "" && h.Type != "Jeu principal" {
				l += " · " + h.Type
			}
			if len(h.Platforms) > 0 {
				l += " · " + strings.Join(h.Platforms, ", ")
			}
			if h.Detail != "" {
				l += " · " + h.Detail
			}
			labels = append(labels, l)
		}
		custom := fmt.Sprintf("Ajouter « %s » sans fiche IGDB", query)
		retry := "Chercher un autre nom…"
		labels = append(labels, custom, retry)
		pick, err := zenity.List("Choisissez le jeu :", labels, zenity.Title(dlgTitle),
			zenity.OKLabel("Choisir"), zenity.CancelLabel("Annuler"), zenity.Width(560), zenity.Height(420), zenity.DisallowEmpty())
		if errors.Is(err, zenity.ErrCanceled) || err != nil {
			return
		}
		if pick == retry {
			continue
		}
		var itemID int
		var title string
		if pick == custom {
			itemID, title, err = api.resolve(0, 0, query)
		} else {
			for i, l := range labels {
				if l == pick && i < len(hits) {
					itemID, title, err = api.resolve(hits[i].ItemID, hits[i].IGDBID, "")
					break
				}
			}
		}
		if err != nil {
			zenity.Error("Impossible d'enregistrer ce choix : "+err.Error(), zenity.Title(dlgTitle))
			return
		}
		if itemID > 0 {
			postInbox(inboxMsg{Type: "resolve", Key: u.Key, ItemID: itemID, Title: title})
		}
		return
	}
}
