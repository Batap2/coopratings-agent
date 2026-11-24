package main

// Rating reminders. After a session of a game not rated yet (the server says
// so: "notify"), a notification offers to rate it, at most once a day per
// game: playing it again the same day shows nothing.
//
// The session where the game is finished may come after that day's
// notification, so a game not played for a week and still without a grade
// becomes "to rate": no notification, but the icon goes to the taskbar with
// its "!" badge, like for a game to identify (taskbar.go), and the tray menu
// and the app window list it. It leaves the list once rated (here or on the
// site), muted ("Ne plus me rappeler pour ce jeu"), removed from the list,
// or played again (the week then starts over).

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"time"
)

const (
	lateReminderDays = 7                // days without playing before a game is "to rate"
	rateCheckEvery   = 6 * time.Hour    // asking the server which games still have no grade
	rateCheckRetry   = 30 * time.Minute // server unreachable: next try
)

// rateReminder is a game the app offered to rate while it had no grade.
type rateReminder struct {
	Title    string `json:"title"`
	Played   string `json:"played"`             // day of its last session (PC date)
	Notified string `json:"notified,omitempty"` // day of the last notification
	ToRate   bool   `json:"toRate,omitempty"`   // not played for a week, still no grade
}

// rateEntry is a game to rate, as listed by the tray and the app window.
type rateEntry struct {
	ItemID int    `json:"itemId"`
	Title  string `json:"title"`
}

// showToastFunc shows notifications (replaced in tests).
var showToastFunc = showToast

func dayOf(t time.Time) string { return t.Format("2006-01-02") }

// offerRating handles a session the server accepted: remembers the game and
// offers to rate it, once a day. A session sent long after it ended
// (offline, logged out) shows nothing: the moment has passed.
func (a *agent) offerRating(r apiSessionResult, q queuedSession, now time.Time) {
	if !r.Notify || r.ItemID <= 0 || a.st.Muted[r.ItemID] {
		return
	}
	rem := a.st.Rate[r.ItemID]
	if rem == nil {
		rem = &rateReminder{}
		a.st.Rate[r.ItemID] = rem
	}
	if r.Title != "" {
		rem.Title = r.Title
	}
	if d := dayOf(q.End); d > rem.Played {
		rem.Played = d
		rem.ToRate = false // played again: the week starts over
	}
	today := dayOf(now)
	if rem.Notified == today || now.Sub(q.End) >= 3*time.Hour {
		return
	}
	rem.Notified = today
	a.notifyRate(r.ItemID, r.Title, formatDuration(q.Seconds)+" de jeu. Voulez-vous le noter ?", q.Seconds)
}

// checkRatings asks the server, every few hours, whether the games not
// played for a week have a grade (they may have been rated on the site):
// those without one become "to rate", the others are forgotten.
func (a *agent) checkRatings(now time.Time) {
	if len(a.st.Rate) == 0 || !a.loggedIn() || now.Before(a.nextRateCheck) {
		return
	}
	limit := dayOf(now.AddDate(0, 0, -lateReminderDays))
	for id, rem := range a.st.Rate {
		if a.st.Muted[id] {
			delete(a.st.Rate, id)
			continue
		}
		if rem.Played > limit {
			continue // played this week: the daily notification is enough
		}
		g, err := a.api.game(id)
		var he *httpError
		switch {
		case errors.As(err, &he) && he.status == http.StatusNotFound:
			delete(a.st.Rate, id) // no longer in the list
		case err == errUnauthorized:
			a.netError(err)
			return
		case err != nil:
			log.Printf("jeux à noter : %v", err)
			a.nextRateCheck = now.Add(rateCheckRetry)
			return
		case g.Grade != nil:
			delete(a.st.Rate, id) // rated meanwhile
		case !rem.ToRate:
			rem.ToRate = true
			if g.Title != "" {
				rem.Title = g.Title
			}
			log.Printf("à noter : %s", rem.Title)
		}
	}
	a.nextRateCheck = now.Add(rateCheckEvery)
}

// toRate lists the games to rate, oldest session first.
func (st *state) toRate() []rateEntry {
	var list []rateEntry
	for id, rem := range st.Rate {
		if rem.ToRate && !st.Muted[id] {
			list = append(list, rateEntry{ItemID: id, Title: rem.Title})
		}
	}
	sort.Slice(list, func(i, j int) bool {
		pi, pj := st.Rate[list[i].ItemID].Played, st.Rate[list[j].ItemID].Played
		return pi < pj || pi == pj && list[i].ItemID < list[j].ItemID
	})
	return list
}

// notifyRate offers to rate a game; seconds (0 = none) is shown in the
// rating window.
func (a *agent) notifyRate(itemID int, title, body string, seconds int) {
	// "Rate" opens the app's small rating window (grade + status) rather than
	// the browser; that window can also open the spreadsheet for comments.
	rate := fmt.Sprintf("%s:rate?item=%d&s=%d", protocolScheme, itemID, seconds)
	mute := fmt.Sprintf("%s:mute?item=%d", protocolScheme, itemID)
	err := showToastFunc(title, body, rate,
		toastAction{Label: "Noter", URL: rate},
		toastAction{Label: "Ne plus me rappeler pour ce jeu", URL: mute})
	if err != nil {
		log.Print(err)
	}
}

// muteGame stops the rating reminders of a game: the main process takes it
// from the inbox and tells the server, retrying later if it is unreachable.
func muteGame(itemID int) {
	postInbox(inboxMsg{Type: "mute", ItemID: itemID})
	if !mutexExists(mutexName) {
		newAPI(loadConfig()).mute(itemID)
	}
}
