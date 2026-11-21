package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeSite records the request bodies sent to each API route and answers
// with canned JSON.
type fakeSite struct {
	bodies    map[string][]string
	playTimes string
}

func newFakeSite(t *testing.T) (*fakeSite, *httptest.Server) {
	f := &fakeSite{bodies: map[string][]string{}, playTimes: playTimesPublic}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.bodies[r.URL.Path] = append(f.bodies[r.URL.Path], string(b))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/agent/login":
			w.Write([]byte(`{"token":"t","username":"u"}`))
		case "/api/agent/me":
			json.NewEncoder(w).Encode(map[string]string{"username": "u", "playTimes": f.playTimes})
		case "/api/agent/identify":
			var in struct{ Candidates []apiCandidate }
			json.Unmarshal(b, &in)
			var out []apiVerdict
			for _, c := range in.Candidates {
				out = append(out, apiVerdict{Key: c.Key, Verdict: "game", ItemID: 7, Title: "Jeu"})
			}
			json.NewEncoder(w).Encode(map[string]any{"verdicts": out})
		case "/api/agent/sessions":
			var in struct{ Sessions []apiSession }
			json.Unmarshal(b, &in)
			var out []apiSessionResult
			for _, s := range in.Sessions {
				out = append(out, apiSessionResult{ID: s.ID, OK: true, ItemID: 7, Title: "Jeu"})
			}
			json.NewEncoder(w).Encode(map[string]any{"results": out})
		default:
			w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func TestLooksLikePath(t *testing.T) {
	for _, n := range []string{`C:\Users\moi\Downloads`, `jeux\hades`, "D:/Jeux/Hades", "Hades - E:\\Jeux"} {
		if !looksLikePath(n) {
			t.Errorf("%q devrait être refusé", n)
		}
	}
	for _, n := range []string{"Hades II", "Fate/stay night", "Ratchet & Clank 3", "Tomb Raider", "Max Payne"} {
		if looksLikePath(n) {
			t.Errorf("%q devrait être accepté", n)
		}
	}
}

// The game folder's name is sent, never a profile folder (the Windows user
// name), the users folder or a drive root.
func TestGameFolderName(t *testing.T) {
	t.Setenv("USERPROFILE", `C:\Users\Quelquun`)
	cases := map[string]string{
		`C:\Users\Quelquun`:                  "",
		`C:\Users\Autre`:                     "",
		`C:\Users`:                           "",
		`D:\`:                                "",
		`C:\Users\Quelquun\Desktop\Hades.II`: "Hades.II",
		`D:\Jeux\Hollow Knight`:              "Hollow Knight",
		`C:\Users\Quelquun\Games\Celeste`:    "Celeste",
	}
	for dir, want := range cases {
		if got := gameFolderName(dir); got != want {
			t.Errorf("gameFolderName(%q) = %q, attendu %q", dir, got, want)
		}
	}
}

// An emulator showing the ROM's path in its title: only the file name is used.
func TestEmulatorTitleWithoutPath(t *testing.T) {
	e := findEmulator(`E:\emu\DeSmuME_0.9.13_x64.exe`)
	got := emulatorGameTitle(e, []string{`DeSmuME 0.9.13 x64 | E:\roms\nds\Pokemon - Platinum Version (USA).nds`})
	if got != "Pokemon - Platinum Version" {
		t.Fatalf("titre %q", got)
	}
}

// Login: the site login and password, nothing about the PC.
func TestLoginSendsNothingAboutThePC(t *testing.T) {
	f, srv := newFakeSite(t)
	if _, _, err := (&apiClient{server: srv.URL, http: srv.Client()}).login("moi", "secret"); err != nil {
		t.Fatal(err)
	}
	var body map[string]string
	json.Unmarshal([]byte(f.bodies["/api/agent/login"][0]), &body)
	if len(body) != 2 || body["login"] != "moi" || body["password"] != "secret" {
		t.Fatalf("connexion : %v", body)
	}
}

// Identification: the server gets a number instead of the exe path and no
// path in the names; the verdict comes back to the local key.
func TestIdentifySendsNoPath(t *testing.T) {
	f, srv := newFakeSite(t)
	a := testAgent(t)
	a.api = &apiClient{server: srv.URL, token: "t", http: srv.Client()}
	key := `c:\users\quelquun\jeux\hades\hades.exe`
	// A path among the names: saved by an older version, before filtering.
	names := []string{"Hades", `C:\Users\quelquun\jeux\hades`}
	a.st.Pending[key] = pendingIdent{Cand: apiCandidate{Key: key, Names: names}, Path: key}
	a.identify(time.Now())

	sent := f.bodies["/api/agent/identify"]
	if len(sent) != 1 {
		t.Fatalf("%d envoi(s)", len(sent))
	}
	if strings.Contains(sent[0], `\\`) || strings.Contains(strings.ToLower(sent[0]), "c:") || strings.Contains(sent[0], "quelquun") {
		t.Fatalf("chemin envoyé : %s", sent[0])
	}
	if v, ok := a.st.Verdicts[key]; !ok || v.ItemID != 7 || len(a.st.Pending) != 0 {
		t.Fatalf("verdict %+v (%v), en attente %d", v, ok, len(a.st.Pending))
	}
}

// Play times off: sessions go with their duration only, no date nor time.
func TestPlayTimesOff(t *testing.T) {
	for _, pt := range []string{playTimesOff, playTimesPublic} {
		f, srv := newFakeSite(t)
		f.playTimes = pt
		a := testAgent(t)
		a.api = &apiClient{server: srv.URL, token: "t", http: srv.Client()}
		start := time.Date(2026, 11, 20, 23, 30, 0, 0, time.Local)
		a.queue = []queuedSession{{ID: "s1", ItemID: 7, Source: "steam", Start: start, End: start.Add(time.Hour), Seconds: 3600}}
		a.flush(time.Now())

		sent := f.bodies["/api/agent/sessions"]
		if len(sent) != 1 || len(a.queue) != 0 {
			t.Fatalf("%s : %d envoi(s), file %d", pt, len(sent), len(a.queue))
		}
		var in struct{ Sessions []map[string]any }
		json.Unmarshal([]byte(sent[0]), &in)
		s := in.Sessions[0]
		_, hasStart := s["start"]
		_, hasEnd := s["end"]
		if pt == playTimesOff && (hasStart || hasEnd || len(s) != 4 || s["id"] != "s1" || s["itemId"] != 7.0 || s["seconds"] != 3600.0) {
			t.Fatalf("horaires désactivés : %v (attendu id, itemId, source, seconds seulement)", s)
		}
		if pt == playTimesPublic && (!hasStart || !hasEnd || s["day"] != nil) {
			t.Fatalf("horaires publics : %v", s)
		}
	}
}
