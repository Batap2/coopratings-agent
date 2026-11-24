package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// reminderSite is a fake site: every session is of an unrated game
// (notify), and /api/agent/game answers item 7 rated, item 8 no longer in
// the list, any other item unrated. *calls counts the /api/agent/game calls.
func reminderSite(t *testing.T, calls *int) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/agent/sessions":
			var in struct {
				Sessions []apiSession `json:"sessions"`
			}
			json.NewDecoder(r.Body).Decode(&in)
			res := []apiSessionResult{}
			for _, s := range in.Sessions {
				res = append(res, apiSessionResult{ID: s.ID, OK: true, ItemID: s.ItemID, Title: "Jeu " + strconv.Itoa(s.ItemID), Notify: true})
			}
			json.NewEncoder(w).Encode(map[string]any{"results": res})
		case "/api/agent/game":
			*calls++
			id, _ := strconv.Atoi(r.URL.Query().Get("item"))
			switch id {
			case 7:
				json.NewEncoder(w).Encode(map[string]any{"itemId": id, "title": "Jeu 7", "grade": 15})
			case 8:
				w.WriteHeader(http.StatusNotFound)
				json.NewEncoder(w).Encode(map[string]string{"error": "ce jeu n'est pas dans votre liste"})
			default:
				json.NewEncoder(w).Encode(map[string]any{"itemId": id, "title": "Jeu " + strconv.Itoa(id)})
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// recordToasts replaces notifications with a list of their titles.
func recordToasts(t *testing.T) *[]string {
	var shown []string
	prev := showToastFunc
	showToastFunc = func(title, body, open string, actions ...toastAction) error {
		shown = append(shown, title)
		return nil
	}
	t.Cleanup(func() { showToastFunc = prev })
	return &shown
}

func reminderAgent(t *testing.T, calls *int) *agent {
	a := testAgent(t)
	srv := reminderSite(t, calls)
	a.api = &apiClient{server: srv.URL, token: "x", http: srv.Client()}
	return a
}

// playAndSend uploads a one-hour session of item id that ended at end.
func playAndSend(a *agent, id int, end, now time.Time) {
	a.queue = append(a.queue, queuedSession{ID: randomID(), Key: "jeu" + strconv.Itoa(id), ItemID: id, Title: "jeu",
		Source: "steam", Start: end.Add(-time.Hour), End: end, Seconds: 3600})
	a.flush(now)
}

// An unrated game is offered for rating once a day, however many sessions.
func TestRatingOfferedOnceADay(t *testing.T) {
	var calls int
	a := reminderAgent(t, &calls)
	shown := recordToasts(t)
	day1 := time.Date(2026, 11, 24, 15, 0, 0, 0, time.Local)

	playAndSend(a, 5, day1, day1.Add(10*time.Second))
	playAndSend(a, 5, day1.Add(2*time.Hour), day1.Add(2*time.Hour+10*time.Second))
	playAndSend(a, 6, day1.Add(3*time.Hour), day1.Add(3*time.Hour+10*time.Second))
	if len(*shown) != 2 || (*shown)[0] != "Jeu 5" || (*shown)[1] != "Jeu 6" {
		t.Fatalf("jour 1 : %v, attendu une notification pour Jeu 5 puis Jeu 6", *shown)
	}

	day2 := day1.AddDate(0, 0, 1)
	playAndSend(a, 5, day2, day2.Add(10*time.Second))
	if len(*shown) != 3 {
		t.Fatalf("jour 2 : %v, attendu une nouvelle notification pour Jeu 5", *shown)
	}
	if rem := a.st.Rate[5]; rem == nil || rem.Played != "2026-11-25" {
		t.Fatalf("dernière partie de Jeu 5 : %+v", rem)
	}

	// Muted from the notification: nothing any more, no last reminder.
	postInbox(inboxMsg{Type: "mute", ItemID: 6})
	a.handleInbox(day2)
	playAndSend(a, 6, day2.Add(time.Hour), day2.Add(time.Hour+10*time.Second))
	if len(*shown) != 3 || a.st.Rate[6] != nil {
		t.Fatalf("jeu coupé : %v, rappel %+v", *shown, a.st.Rate[6])
	}
}

// A game not played for a week and still without a grade becomes "to rate"
// (icon in the taskbar, no notification) until rated, muted or played again.
func TestGamesToRateAfterAWeek(t *testing.T) {
	var calls int
	a := reminderAgent(t, &calls)
	shown := recordToasts(t)
	now := time.Date(2026, 11, 24, 20, 0, 0, 0, time.Local)
	daysAgo := func(n int) string { return dayOf(now.AddDate(0, 0, -n)) }
	a.st.Rate[5] = &rateReminder{Title: "jeu 5", Played: daysAgo(8)}
	a.st.Rate[6] = &rateReminder{Title: "jeu 6", Played: daysAgo(6)}  // not a week yet
	a.st.Rate[7] = &rateReminder{Title: "jeu 7", Played: daysAgo(10)} // rated on the site meanwhile
	a.st.Rate[8] = &rateReminder{Title: "jeu 8", Played: daysAgo(9)}  // removed from the list
	a.st.Rate[9] = &rateReminder{Title: "jeu 9", Played: daysAgo(7)}
	toRate := func() (titles []string) {
		for _, g := range a.st.toRate() {
			titles = append(titles, g.Title)
		}
		return titles
	}

	a.checkRatings(now)
	if got := toRate(); len(got) != 2 || got[0] != "Jeu 5" || got[1] != "Jeu 9" {
		t.Fatalf("à noter : %v, attendu [Jeu 5 Jeu 9]", got)
	}
	if a.st.Rate[7] != nil || a.st.Rate[8] != nil || a.st.Rate[6] == nil || a.st.Rate[6].ToRate {
		t.Fatalf("restant : %v", a.st.Rate)
	}
	if len(*shown) != 0 {
		t.Fatalf("aucune notification attendue : %v", *shown)
	}

	// The server is asked again only a few hours later.
	n := calls
	a.checkRatings(now.Add(time.Hour))
	if calls != n {
		t.Errorf("site interrogé à nouveau au bout d'une heure")
	}

	// Rated from the rating window.
	postInbox(inboxMsg{Type: "rated", ItemID: 5})
	a.handleInbox(now)
	if got := toRate(); len(got) != 1 || got[0] != "Jeu 9" {
		t.Fatalf("après la note de Jeu 5 : %v", got)
	}

	// Played again: no longer to rate, the week starts over.
	later := now.AddDate(0, 0, 2)
	playAndSend(a, 9, later.Add(-time.Minute), later)
	if got := toRate(); len(got) != 0 {
		t.Fatalf("Jeu 9 rejoué : encore à noter %v", got)
	}
	a.checkRatings(later.AddDate(0, 0, 6))
	if got := toRate(); len(got) != 1 || got[0] != "Jeu 6" {
		t.Fatalf("6 jours après : %v, attendu [Jeu 6]", got)
	}
	a.checkRatings(later.AddDate(0, 0, 7))
	if got := toRate(); len(got) != 2 || got[1] != "Jeu 9" {
		t.Fatalf("une semaine après : %v, attendu [Jeu 6 Jeu 9]", got)
	}

	// "Ne plus me rappeler" from the rating window.
	postInbox(inboxMsg{Type: "mute", ItemID: 6})
	a.handleInbox(later)
	if got := toRate(); len(got) != 1 || got[0] != "Jeu 9" {
		t.Fatalf("après « Ne plus me rappeler » : %v", got)
	}
}

// Server unreachable: the games wait and the check is tried again later.
func TestGamesToRateWaitForServer(t *testing.T) {
	a := testAgent(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close() // nothing listens any more
	a.api = &apiClient{server: srv.URL, token: "x", http: &http.Client{Timeout: time.Second}}
	now := time.Now()
	a.st.Rate[5] = &rateReminder{Title: "Jeu 5", Played: dayOf(now.AddDate(0, 0, -8))}

	a.checkRatings(now)
	if a.st.Rate[5] == nil || a.st.Rate[5].ToRate || !a.nextRateCheck.After(now) || a.nextRateCheck.After(now.Add(time.Hour)) {
		t.Fatalf("hors ligne : %+v, nouvel essai %v", a.st.Rate[5], a.nextRateCheck)
	}
}
