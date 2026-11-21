package main

// Main loop: every minute, detect running games, have the server identify
// them, track play sessions, upload finished ones and offer to rate the game.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

var minSessionSeconds = 5 * 60 // shorter sessions are ignored (launched to update, crash…); see --min-session

func randomID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

type agent struct {
	cfg        config
	api        *apiClient
	st         *state
	queue      []queuedSession
	det        *detector
	wake       windows.Handle
	interval   time.Duration
	lastTick   time.Time
	resumeAt   time.Time // end of sleep: no session may start before this
	offline    bool
	lastSync   time.Time
	lastDetect time.Time
	authWarned bool // "session expired" already notified
	authBad    bool // token rejected by the server: waiting for a new login
	quit       bool
	dev        bool

	// Auto-update (update.go)
	nextUpdate time.Time
	updating   bool // check/download running in the background
	updateCh   chan updateResult
	update     *updateReady // downloaded; installed as soon as no game is running
	restart    bool         // start the new version on exit
}

type updateResult struct {
	u   *updateReady
	err error
}

func newAgent(cfg config, interval time.Duration, dev bool) *agent {
	a := &agent{
		cfg:        cfg,
		api:        newAPI(cfg),
		st:         loadState(),
		queue:      loadQueue(),
		det:        newDetector(),
		wake:       createWakeEvent(),
		interval:   interval,
		dev:        dev,
		nextUpdate: time.Now().Add(updateFirst),
		updateCh:   make(chan updateResult, 1),
	}
	a.seedHistory()
	a.restoreActive(time.Now())
	return a
}

// restoreActive picks up sessions left active by a previous run. If the app
// was restarted right away (update, Quit then relaunch), the session goes on
// without losing the earlier part. Otherwise (PC shut down, app closed for a
// long time) it ends at its last observation.
func (a *agent) restoreActive(now time.Time) {
	for id, s := range a.st.Active {
		if now.Sub(s.LastSeen) > a.maxGap() {
			a.finish(s, s.LastSeen)
			delete(a.st.Active, id)
		}
	}
}

// maxGap is the largest gap between two observations that still counts as
// the same session (beyond it: sleep, app stopped).
func (a *agent) maxGap() time.Duration { return 3*a.interval + time.Minute }

func (a *agent) run(once bool) {
	log.Printf("démarrage (version %s, serveur %s, %s)", version, a.cfg.Server, a.cfg.Username)
	for {
		a.tick()
		if once || a.quit { // Quit: stop now, without waiting for the next tick
			break
		}
		sleepOrWake(a.wake, a.interval)
	}
	if a.quit {
		a.flush(time.Now()) // upload whatever can be sent before exiting
	}
	a.st.save()
	saveQueue(a.queue)
	a.writeStatus()
	log.Printf("arrêt")
	if a.restart {
		startUpdated()
	}
}

func (a *agent) tick() {
	now := time.Now()
	a.handleInbox(now)
	if a.quit {
		return
	}
	// A long gap since the last tick means sleep/hibernation: sessions end at
	// the last tick seen and nothing is counted in between.
	if !a.lastTick.IsZero() && now.Sub(a.lastTick) > a.maxGap() {
		a.closeAll(a.lastTick)
		a.resumeAt = now
	}
	a.lastTick = now

	cands, _ := a.det.scan()
	for _, c := range cands {
		if _, ok := a.st.Verdicts[c.Key]; !ok {
			a.st.Pending[c.Key] = pendingIdent{Cand: apiCandidate{Key: c.Key, SteamAppID: c.SteamAppID, Names: c.Names, Platforms: c.Platforms}, Path: c.Path}
		}
	}
	a.identify(now)
	a.track(cands, now)
	a.flush(now)
	a.flushMutes()
	a.flushRevokes(now)
	a.refreshDetectable()
	a.syncTrayIcon(len(a.st.Unknown) > 0 || !a.loggedIn()) // before save: st.TrayShown
	a.st.save()
	saveQueue(a.queue)
	a.writeStatus()
	tray.update(a.st, a.cfg.Username, a.loggedIn(), a.offline)
	a.maybeUpdate(now)
}

// maybeUpdate checks for a new version in the background, then installs it
// and restarts the app when no game is running.
func (a *agent) maybeUpdate(now time.Time) {
	if (a.dev && !updateTest) || !isInstalledCopy() {
		return
	}
	select {
	case r := <-a.updateCh:
		a.updating = false
		switch {
		case r.err != nil:
			log.Printf("mise à jour : %v", r.err)
			a.nextUpdate = now.Add(updateRetry)
		case r.u != nil:
			a.update = r.u
			log.Printf("version %s téléchargée et vérifiée", r.u.Version)
		}
	default:
	}
	if a.update == nil && !a.updating && !now.Before(a.nextUpdate) {
		a.updating = true
		a.nextUpdate = now.Add(updateEvery)
		go func() {
			u, err := checkUpdate()
			a.updateCh <- updateResult{u, err}
		}()
	}
	if a.update == nil || len(a.st.Active) > 0 {
		return // never in the middle of a game
	}
	if err := installUpdate(a.update); err != nil {
		log.Printf("mise à jour %s : installation impossible : %v", a.update.Version, err)
		a.update = nil
		a.nextUpdate = now.Add(updateRetry)
		return
	}
	log.Printf("mise à jour vers la version %s : relance", a.update.Version)
	a.restart, a.quit = true, true
}

// --- Identification ----------------------------------------------------------

func (a *agent) identify(now time.Time) {
	if len(a.st.Pending) == 0 || !a.loggedIn() {
		return
	}
	var batch []apiCandidate
	for _, p := range a.st.Pending {
		batch = append(batch, p.Cand)
		if len(batch) == 20 {
			break
		}
	}
	verdicts, err := a.api.identify(batch)
	if err != nil {
		a.netError(err)
		return
	}
	a.offline = false
	for _, v := range verdicts {
		p, ok := a.st.Pending[v.Key]
		if !ok {
			continue
		}
		delete(a.st.Pending, v.Key)
		a.st.Verdicts[v.Key] = verdict{Verdict: v.Verdict, ItemID: v.ItemID, IGDBID: v.IGDBID, Title: v.Title, At: now}
		log.Printf("identifié %q → %s %s (%s)", firstName(p.Cand), v.Verdict, v.Title, v.Reason)
		switch v.Verdict {
		case "unknown":
			a.st.Unknown[v.Key] = unknownGame{Key: v.Key, Name: firstName(p.Cand), Path: p.Path, FirstSeen: now}
		case "notgame":
			delete(a.st.Active, "key:"+v.Key)
		}
	}
}

func firstName(c apiCandidate) string {
	if len(c.Names) > 0 {
		return c.Names[0]
	}
	return c.Key
}

// identityOf returns what a candidate's playtime is attributed to.
// An empty id means it is not a game (server verdict).
func (a *agent) identityOf(c candidate) (id, title string) {
	v, ok := a.st.Verdicts[c.Key]
	title = c.Key
	if len(c.Names) > 0 {
		title = c.Names[0]
	}
	switch {
	case !ok, v.Verdict == "unknown":
		return "key:" + c.Key, title
	case v.Verdict == "notgame":
		return "", ""
	case v.ItemID > 0:
		return "item:" + strconv.Itoa(v.ItemID), v.Title
	case v.IGDBID > 0:
		return "igdb:" + strconv.Itoa(v.IGDBID), v.Title
	}
	return "key:" + c.Key, title
}

// --- Sessions ----------------------------------------------------------------

func (a *agent) track(cands []candidate, now time.Time) {
	seen := map[string]bool{}
	for _, c := range cands {
		id, title := a.identityOf(c)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		s := a.st.Active[id]
		// The game has just been identified: the session started under its
		// detection key continues under its real identity.
		if s == nil && !strings.HasPrefix(id, "key:") {
			if old := a.st.Active["key:"+c.Key]; old != nil {
				delete(a.st.Active, "key:"+c.Key)
				old.Identity, old.Title = id, title
				a.st.Active[id] = old
				s = old
			}
		}
		if s == nil {
			start := c.Started
			if start.IsZero() || start.After(now) || now.Sub(start) > 6*time.Hour {
				start = now
			}
			// Never before the end of a sleep nor before the end of the
			// previous session of the same game: otherwise, after a sleep or
			// an app restart, time already uploaded would count twice.
			for _, t := range []time.Time{a.resumeAt, a.st.LastEnd[id], a.st.LastEnd["key:"+c.Key]} {
				if start.Before(t) {
					start = t
				}
			}
			platform := ""
			if len(c.Platforms) > 0 {
				platform = c.Platforms[0]
			}
			s = &activeSession{Identity: id, Key: c.Key, Title: title, Platform: platform, Source: c.Source, Start: start}
			a.st.Active[id] = s
			log.Printf("début : %s (%s, depuis %s)", title, c.Source, start.Format("15:04"))
		}
		s.LastSeen = now
	}
	for id, s := range a.st.Active {
		if !seen[id] {
			a.finish(s, s.LastSeen)
			delete(a.st.Active, id)
		}
	}
}

func (a *agent) closeAll(end time.Time) {
	for id, s := range a.st.Active {
		a.finish(s, end)
		delete(a.st.Active, id)
	}
}

// finish queues a finished session for upload (if long enough).
func (a *agent) finish(s *activeSession, end time.Time) {
	a.st.LastEnd[s.Identity] = end
	a.st.LastEnd["key:"+s.Key] = end
	secs := int(end.Sub(s.Start).Seconds())
	q := queuedSession{ID: randomID(), Key: s.Key, Title: s.Title, Platform: s.Platform, Source: s.Source, Start: s.Start, End: end, Seconds: secs}
	switch {
	case strings.HasPrefix(s.Identity, "item:"):
		q.ItemID, _ = strconv.Atoi(s.Identity[5:])
	case strings.HasPrefix(s.Identity, "igdb:"):
		q.IGDBID, _ = strconv.Atoi(s.Identity[5:])
	}
	if secs < minSessionSeconds {
		log.Printf("fin : %s (%d s, trop court, ignoré)", s.Title, secs)
		if secs >= historyMinSeconds {
			a.record(historyEntry{Key: s.Key, Title: s.Title, ItemID: q.ItemID, End: end, Seconds: secs, Outcome: outShort})
		}
		return
	}
	if u, ok := a.st.Unknown[s.Key]; ok && q.ItemID == 0 && q.IGDBID == 0 {
		u.Seconds += secs
		a.st.Unknown[s.Key] = u
	}
	a.queue = append(a.queue, q)
	a.record(historyEntry{ID: q.ID, Key: q.Key, Title: q.Title, ItemID: q.ItemID, End: end, Seconds: secs, Outcome: outPending})
	log.Printf("fin : %s (%s)", s.Title, formatDuration(secs))
}

// flush uploads finished sessions whose game has been identified.
func (a *agent) flush(now time.Time) {
	if !a.loggedIn() || len(a.queue) == 0 {
		return
	}
	var send []apiSession
	keep := a.queue[:0]
	for _, q := range a.queue {
		if q.ItemID == 0 && q.IGDBID == 0 {
			v, ok := a.st.Verdicts[q.Key]
			switch {
			case !ok || v.Verdict == "unknown":
				keep = append(keep, q) // waiting for the server or manual identification
				continue
			case v.Verdict == "notgame":
				a.setOutcome(q.ID, outNotGame)
				continue // dropped
			}
			q.ItemID, q.IGDBID = v.ItemID, v.IGDBID
			if v.Title != "" {
				q.Title = v.Title
			}
		}
		keep = append(keep, q)
		if len(send) < 50 {
			send = append(send, apiSession{
				ID: q.ID, ItemID: q.ItemID, IGDBID: q.IGDBID, Platform: q.Platform, Source: q.Source,
				Start: q.Start.Format(time.RFC3339), End: q.End.Format(time.RFC3339), Seconds: q.Seconds,
			})
		}
	}
	a.queue = keep
	if len(send) == 0 {
		return
	}
	results, err := a.api.sendSessions(send)
	if err != nil {
		a.netError(err)
		return
	}
	a.offline, a.lastSync = false, now
	byID := map[string]apiSessionResult{}
	for _, r := range results {
		byID[r.ID] = r
	}
	notified := map[int]bool{}
	keep = a.queue[:0]
	for _, q := range a.queue {
		r, ok := byID[q.ID]
		if !ok {
			keep = append(keep, q)
			continue
		}
		e := a.entry(q.ID)
		if e == nil {
			e = &historyEntry{} // sent by a version without history
		}
		if !r.OK {
			q.Attempts++
			log.Printf("envoi refusé : %s : %s (essai %d)", q.Title, r.Error, q.Attempts)
			e.Error = r.Error
			if q.Attempts < 20 {
				keep = append(keep, q)
			} else {
				e.Outcome = outRefused
			}
			continue
		}
		log.Printf("envoyé : %s (%s) %s", r.Title, formatDuration(q.Seconds), r.Note)
		e.Outcome, e.Error = outHours, ""
		switch {
		case r.Ignored:
			e.Outcome = outRemoved
		case r.Added:
			e.Outcome = outAdded
		}
		if r.ItemID > 0 {
			e.ItemID = r.ItemID
		}
		if r.Title != "" {
			e.Title = r.Title
		}
		// The server may have created the game: remember its item id.
		if r.ItemID > 0 {
			if v, ok := a.st.Verdicts[q.Key]; ok && v.ItemID == 0 {
				v.ItemID, v.IGDBID, v.Title = r.ItemID, 0, r.Title
				a.st.Verdicts[q.Key] = v
			}
		}
		if r.Notify && !a.st.Muted[r.ItemID] && !notified[r.ItemID] && now.Sub(q.End) < 3*time.Hour {
			notified[r.ItemID] = true
			a.notifyRate(r, q)
		}
	}
	a.queue = keep
}

func (a *agent) notifyRate(r apiSessionResult, q queuedSession) {
	// "Rate" opens the app's small rating window (grade + status) rather than
	// the browser; that window can also open the spreadsheet for comments.
	rate := fmt.Sprintf("%s:rate?item=%d&s=%d", protocolScheme, r.ItemID, q.Seconds)
	mute := fmt.Sprintf("%s:mute?item=%d", protocolScheme, r.ItemID)
	err := showToast(r.Title, formatDuration(q.Seconds)+" de jeu. Voulez-vous le noter ?", rate,
		toastAction{Label: "Noter", URL: rate},
		toastAction{Label: "Ne plus me rappeler pour ce jeu", URL: mute})
	if err != nil {
		log.Print(err)
	}
}

func formatDuration(secs int) string {
	m := (secs + 30) / 60
	if m < 60 {
		return fmt.Sprintf("%d min", m)
	}
	if m%60 == 0 {
		return fmt.Sprintf("%d h", m/60)
	}
	return fmt.Sprintf("%d h %02d", m/60, m%60)
}

// loggedIn reports whether a token exists and the server has not rejected it.
// While logged out the app keeps tracking games: sessions wait on disk and
// are uploaded once logged in.
func (a *agent) loggedIn() bool { return a.api.token != "" && !a.authBad }

func (a *agent) netError(err error) {
	if err == errUnauthorized {
		a.authBad = true
		if !a.authWarned {
			a.authWarned = true
			log.Print("jeton refusé : reconnexion nécessaire")
			remindLogin()
		}
		return
	}
	if !a.offline {
		log.Printf("serveur injoignable (%v) : sessions gardées pour plus tard", err)
	}
	a.offline = true
}

// --- Commands from other processes -------------------------------------------

func (a *agent) handleInbox(now time.Time) {
	for _, m := range readInbox() {
		switch m.Type {
		case "revoke":
			if m.Token != "" {
				a.st.Revoke = append(a.st.Revoke, pendingRevoke{Token: m.Token, At: now})
			}
		case "mute":
			a.st.Muted[m.ItemID] = true
			a.st.PendingMutes = append(a.st.PendingMutes, m.ItemID)
			log.Printf("rappels coupés pour le jeu %d", m.ItemID)
		case "resolve":
			a.st.Verdicts[m.Key] = verdict{Verdict: "game", ItemID: m.ItemID, Title: m.Title, At: now}
			delete(a.st.Unknown, m.Key)
			delete(a.st.Pending, m.Key)
			for i := range a.queue {
				if a.queue[i].Key == m.Key && a.queue[i].ItemID == 0 {
					a.queue[i].ItemID, a.queue[i].Title = m.ItemID, m.Title
					if e := a.entry(a.queue[i].ID); e != nil {
						e.ItemID, e.Title = m.ItemID, m.Title
					}
				}
			}
			if s := a.st.Active["key:"+m.Key]; s != nil {
				delete(a.st.Active, "key:"+m.Key)
				s.Identity, s.Title = "item:"+strconv.Itoa(m.ItemID), m.Title
				a.st.Active[s.Identity] = s
			}
			log.Printf("identifié à la main : %s → %s", m.Key, m.Title)
		case "notgame":
			a.st.Verdicts[m.Key] = verdict{Verdict: "notgame", At: now}
			delete(a.st.Unknown, m.Key)
			delete(a.st.Pending, m.Key)
			delete(a.st.Active, "key:"+m.Key)
			keep := a.queue[:0]
			for _, q := range a.queue {
				if q.Key != m.Key || q.ItemID > 0 || q.IGDBID > 0 {
					keep = append(keep, q)
				} else {
					a.setOutcome(q.ID, outNotGame)
				}
			}
			a.queue = keep
			log.Printf("pas un jeu : %s", m.Key)
		case "reload", "logout":
			// Login or logout done from a window: reload the configuration.
			// When logged out the app keeps running and offers to log in
			// again from its tray icon.
			a.cfg = loadConfig()
			a.api = newAPI(a.cfg)
			a.authWarned, a.authBad = false, false
			log.Printf("connexion : %q", a.cfg.Username)
		case "showlogin":
			go tray.login()
		case "quit":
			// Active sessions are NOT finished here: kept in the state, they
			// continue if the app is restarted soon (restoreActive), otherwise
			// they end at their last observation. Finishing them here would
			// drop a pre-restart segment shorter than minSessionSeconds.
			a.quit = true
		}
	}
}

func (a *agent) flushMutes() {
	if len(a.st.PendingMutes) == 0 || !a.loggedIn() {
		return
	}
	var keep []int
	for _, id := range a.st.PendingMutes {
		if err := a.api.mute(id); err != nil {
			keep = append(keep, id)
		}
	}
	a.st.PendingMutes = keep
}

// flushRevokes revokes on the server the tokens dropped by a logout or a new
// login. Retried until the server answers; given up once the token has
// expired anyway.
func (a *agent) flushRevokes(now time.Time) {
	if len(a.st.Revoke) == 0 {
		return
	}
	var keep []pendingRevoke
	for _, r := range a.st.Revoke {
		if now.Sub(r.At) > tokenLifetime {
			continue
		}
		if err := revokeToken(a.cfg.Server, r.Token); err != nil && err != errUnauthorized {
			keep = append(keep, r)
			continue
		}
		log.Print("ancien jeton révoqué sur le site")
	}
	a.st.Revoke = keep
}

// refreshDetectable refreshes the list of known game executables (weekly).
func (a *agent) refreshDetectable() {
	if !a.det.disc.stale() || time.Since(a.lastDetect) < 6*time.Hour || !a.loggedIn() {
		return
	}
	a.lastDetect = time.Now()
	if err := a.api.downloadDetectable(appFile("detectable.json")); err != nil {
		log.Printf("liste des jeux connus : %v", err)
		return
	}
	a.det.disc = loadDetectable()
	log.Printf("liste des jeux connus : %d exe", len(a.det.disc.byExe)+len(a.det.disc.names))
}

func (a *agent) writeStatus() {
	st := status{
		Version: version, Username: a.cfg.Username, Queue: len(a.queue), Unknown: len(a.st.Unknown),
		LastSync: a.lastSync, Offline: a.offline, At: time.Now(),
		Recent: a.recent(), MinSession: minSessionSeconds,
	}
	for _, s := range a.st.Active {
		st.Playing = append(st.Playing, s.Title)
	}
	writeJSON(appFile("status.json"), st)
}

// --- Log ---------------------------------------------------------------------

func setupLog() {
	path := appFile("agent.log")
	if st, err := os.Stat(path); err == nil && st.Size() > 1<<20 {
		os.Rename(path, path+".old")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	log.SetOutput(f)
	log.SetFlags(log.Ldate | log.Ltime)
}
