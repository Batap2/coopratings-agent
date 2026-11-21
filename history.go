package main

// Recent activity: what happened to each finished session (sent, waiting,
// ignored), listed in the control window.

import (
	"fmt"
	"time"
)

const (
	historyKeep       = 30 // entries kept in state.json
	historyShow       = 15 // entries listed in the control window
	historyMinSeconds = 60 // ignored sessions shorter than this are not listed
)

// Session outcomes.
const (
	outPending = "pending" // queued, not sent yet
	outAdded   = "added"   // sent: the game was added to the list
	outHours   = "hours"   // sent: hours added to a game already in the list
	outShort   = "short"   // shorter than minSessionSeconds: not sent
	outNotGame = "notgame" // not a game (server verdict or user): not sent
	outRemoved = "removed" // game removed from the list by the user: ignored by the server
	outRefused = "refused" // rejected by the server too many times
)

type historyEntry struct {
	ID      string    `json:"id,omitempty"` // queued session id
	Key     string    `json:"key"`
	Title   string    `json:"title"`
	ItemID  int       `json:"itemId,omitempty"`
	End     time.Time `json:"end"`
	Seconds int       `json:"seconds"`
	Outcome string    `json:"outcome"`
	Error   string    `json:"error,omitempty"` // last server error
}

func (a *agent) record(e historyEntry) {
	h := append(a.st.History, e)
	if len(h) > historyKeep {
		h = h[len(h)-historyKeep:]
	}
	a.st.History = h
}

// entry returns the history entry of a queued session, or nil.
func (a *agent) entry(id string) *historyEntry {
	for i := range a.st.History {
		if a.st.History[i].ID == id {
			return &a.st.History[i]
		}
	}
	return nil
}

func (a *agent) setOutcome(id, outcome string) {
	if e := a.entry(id); e != nil {
		e.Outcome = outcome
	}
}

// seedHistory lists sessions queued by a version without history.
func (a *agent) seedHistory() {
	for _, q := range a.queue {
		if a.entry(q.ID) == nil {
			a.record(historyEntry{ID: q.ID, Key: q.Key, Title: q.Title, ItemID: q.ItemID, End: q.End, Seconds: q.Seconds, Outcome: outPending})
		}
	}
}

// recentEntry is a history entry as listed in the control window.
type recentEntry struct {
	Title   string    `json:"title"`
	ItemID  int       `json:"itemId,omitempty"`
	End     time.Time `json:"end"`
	Seconds int       `json:"seconds"`
	// Outcome, or for a pending session why it is waiting:
	// login | identify | checking | offline | retry | sending.
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

// recent returns the latest entries, newest first.
func (a *agent) recent() []recentEntry {
	queued := map[string]queuedSession{}
	for _, q := range a.queue {
		queued[q.ID] = q
	}
	out := []recentEntry{}
	for i := len(a.st.History) - 1; i >= 0 && len(out) < historyShow; i-- {
		e := a.st.History[i]
		r := recentEntry{Title: e.Title, ItemID: e.ItemID, End: e.End, Seconds: e.Seconds, State: e.Outcome, Error: e.Error}
		if e.Outcome == outPending {
			q, ok := queued[e.ID]
			if !ok {
				continue
			}
			v, known := a.st.Verdicts[q.Key]
			unidentified := q.ItemID == 0 && q.IGDBID == 0 && (!known || v.Verdict == "unknown")
			switch {
			case !a.loggedIn():
				r.State = "login"
			case unidentified && known:
				r.State = "identify"
			case a.offline:
				r.State = "offline"
			case unidentified:
				r.State = "checking"
			case e.Error != "":
				r.State = "retry"
			default:
				r.State = "sending"
			}
		}
		out = append(out, r)
	}
	return out
}

// recentLabel describes an entry in the classic control window (the web one
// has its own labels in ui/app.html).
func recentLabel(r recentEntry, minSession int) string {
	switch r.State {
	case outAdded:
		return "ajouté à votre liste"
	case outHours:
		return "temps de jeu ajouté"
	case outRemoved:
		return "ignorée, jeu retiré de votre liste"
	case outShort:
		return fmt.Sprintf("ignorée, moins de %d min", (orInt(minSession, minSessionSeconds)+30)/60)
	case outNotGame:
		return "ignorée, pas un jeu"
	case outRefused:
		return "refusée par le site : " + r.Error
	case "login":
		return "en attente, connectez-vous"
	case "identify":
		return "en attente, jeu à identifier"
	case "checking":
		return "en attente, recherche du jeu"
	case "offline":
		return "en attente, site injoignable"
	case "retry":
		return "en attente : " + r.Error
	}
	return "en attente d'envoi"
}
