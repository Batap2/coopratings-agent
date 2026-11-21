package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testAgent returns an agent without network or detection, in a temp folder.
func testAgent(t *testing.T) *agent {
	t.Helper()
	useDevNames() // don't wake the installed app
	appDir = t.TempDir()
	os.MkdirAll(filepath.Join(appDir, "inbox"), 0o755)
	return &agent{st: loadState(), interval: time.Minute, api: newAPI(config{})}
}

// Quitting and restarting the app mid-game must not split the session: the
// part played before the restart is kept, even if shorter than 5 minutes.
func TestSessionSurvivesQuickRestart(t *testing.T) {
	a := testAgent(t)
	t0 := time.Now().Add(-4 * time.Minute)
	game := candidate{Key: `c:\jeux\jeu.exe`, Started: t0, Source: "steam", Names: []string{"Jeu"}}
	a.track([]candidate{game}, t0.Add(time.Minute))
	a.track([]candidate{game}, t0.Add(3*time.Minute))

	postInbox(inboxMsg{Type: "quit"})
	a.handleInbox(time.Now())
	if !a.quit {
		t.Fatal("quit non pris en compte")
	}
	if len(a.queue) != 0 || len(a.st.Active) != 1 {
		t.Fatalf("Quitter ne doit pas terminer la session : file %d, en cours %d", len(a.queue), len(a.st.Active))
	}
	a.st.save()

	// Restart a minute later: same session, same start.
	b := testAgent2(t)
	now := time.Now()
	b.restoreActive(now)
	b.track([]candidate{game}, now)
	s := b.st.Active["key:"+game.Key]
	if s == nil || !s.Start.Equal(t0) || len(b.queue) != 0 {
		t.Fatalf("la session aurait dû continuer depuis %v : %+v (file %d)", t0, s, len(b.queue))
	}
}

// App restarted long after (PC was off): the session ends at its last
// observation; time without the app running is not counted.
func TestSessionEndsAfterLongGap(t *testing.T) {
	a := testAgent(t)
	t0 := time.Now().Add(-3 * time.Hour)
	game := candidate{Key: `c:\jeux\jeu.exe`, Started: t0, Source: "steam", Names: []string{"Jeu"}}
	a.track([]candidate{game}, t0.Add(time.Minute))
	a.track([]candidate{game}, t0.Add(40*time.Minute))
	a.st.save()

	b := testAgent2(t)
	b.restoreActive(time.Now())
	if len(b.st.Active) != 0 || len(b.queue) != 1 {
		t.Fatalf("en cours %d, file %d : la session aurait dû se terminer", len(b.st.Active), len(b.queue))
	}
	if q := b.queue[0]; q.Seconds != 40*60 {
		t.Fatalf("durée %d s, attendu %d s (jusqu'à la dernière observation)", q.Seconds, 40*60)
	}
}

// testAgent2 returns a new instance that reloads the state from the same folder.
func testAgent2(t *testing.T) *agent {
	t.Helper()
	return &agent{st: loadState(), interval: time.Minute, api: newAPI(config{})}
}
