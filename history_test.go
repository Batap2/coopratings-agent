package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// Each finished session ends up in the recent activity with what happened to
// it: added to the list, time added, ignored, or why it is still waiting.
func TestRecentActivity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Sessions []apiSession `json:"sessions"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		res := []apiSessionResult{}
		for _, s := range in.Sessions {
			out := apiSessionResult{ID: s.ID, OK: true, ItemID: s.ItemID, Title: "Jeu " + strconv.Itoa(s.ItemID)}
			switch s.ItemID {
			case 1:
				out.Added = true
			case 3:
				out.Ignored = true
			case 4:
				out.OK, out.Error = false, "jeu introuvable"
			}
			res = append(res, out)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"results": res})
	}))
	defer srv.Close()

	a := testAgent(t)
	a.api = &apiClient{server: srv.URL, token: "x", http: srv.Client()}
	now := time.Now()
	play := func(identity, key string, d time.Duration) {
		a.finish(&activeSession{Identity: identity, Key: key, Title: key, Start: now.Add(-d)}, now)
	}
	for id := 1; id <= 4; id++ {
		play("item:"+strconv.Itoa(id), "jeu"+strconv.Itoa(id), time.Hour)
	}
	play("key:court", "court", 2*time.Minute)
	play("key:minuscule", "minuscule", 20*time.Second)
	play("key:inconnu", "inconnu", time.Hour)
	play("key:logiciel", "logiciel", time.Hour)
	a.st.Verdicts["inconnu"] = verdict{Verdict: "unknown"}
	a.st.Verdicts["logiciel"] = verdict{Verdict: "notgame"}
	a.flush(now)

	got := map[string]string{}
	for _, r := range a.recent() {
		got[r.Title] = r.State
	}
	want := map[string]string{
		"Jeu 1":    outAdded,
		"Jeu 2":    outHours,
		"Jeu 3":    outRemoved,
		"jeu4":     "retry",
		"court":    outShort,
		"inconnu":  "identify",
		"logiciel": outNotGame,
	}
	if len(got) != len(want) {
		t.Errorf("%d entrées, attendu %d : %v", len(got), len(want), got)
	}
	for title, state := range want {
		if got[title] != state {
			t.Errorf("%s : %q, attendu %q", title, got[title], state)
		}
	}

	// Server unreachable or logged out: the reason is shown.
	a.offline = true
	if s := stateOf(a, "jeu4"); s != "offline" {
		t.Errorf("hors ligne : %q", s)
	}
	a.api.token = ""
	if s := stateOf(a, "jeu4"); s != "login" {
		t.Errorf("déconnecté : %q", s)
	}

	// Kept across restarts, newest first, capped.
	for i := 0; i < historyKeep; i++ {
		a.record(historyEntry{Title: "x", Outcome: outHours})
	}
	a.st.save()
	b := testAgent2(t)
	if len(b.st.History) != historyKeep {
		t.Errorf("historique : %d entrées, attendu %d", len(b.st.History), historyKeep)
	}
}

func stateOf(a *agent, title string) string {
	for _, r := range a.recent() {
		if r.Title == title {
			return r.State
		}
	}
	return ""
}
