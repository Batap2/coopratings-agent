package main

import (
	"os/exec"
	"testing"
	"time"
)

// A watched game that closes brings the next scan forward to exitDelay after
// its exit, and its session's last observation becomes the exit time.
func TestScanSoonAfterGameExits(t *testing.T) {
	a := testAgent(t)
	a.interval = 20 * time.Second // a missed exit would only end the wait here
	defer func(d time.Duration) { exitDelay = d }(exitDelay)
	exitDelay = 300 * time.Millisecond

	cmd := exec.Command("ping", "-n", "2", "127.0.0.1") // exits after ~1 s
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	now := time.Now()
	game := candidate{Key: `c:\jeux\jeu.exe`, PID: uint32(cmd.Process.Pid), Started: now, Source: "steam", Names: []string{"Jeu"}}
	a.track([]candidate{game}, now)
	a.watchGames([]candidate{game})
	if len(a.procs) != 1 {
		t.Fatalf("processus surveillés : %d, attendu 1", len(a.procs))
	}

	start := time.Now()
	a.wait()
	end := time.Now()
	if took := end.Sub(start); took > 5*time.Second {
		t.Fatalf("scan après %v : la fermeture du jeu n'a pas été vue", took)
	}
	s := a.st.Active["key:"+game.Key]
	if s == nil || len(a.procs) != 0 {
		t.Fatalf("session %+v, processus surveillés %d", s, len(a.procs))
	}
	if gap := end.Sub(s.LastSeen); gap < exitDelay-50*time.Millisecond || gap > exitDelay+time.Second {
		t.Fatalf("dernière observation %v avant le scan, attendu ~%v (heure de fermeture)", gap, exitDelay)
	}

	// The scan that follows ends the session at the exit time.
	exitAt := s.LastSeen
	a.track(nil, end)
	if len(a.queue) != 0 || len(a.st.Active) != 0 {
		// Shorter than minSessionSeconds: ignored, but ended.
		t.Fatalf("file %d, en cours %d", len(a.queue), len(a.st.Active))
	}
	if !a.st.LastEnd["key:"+game.Key].Equal(exitAt) {
		t.Fatalf("fin %v, attendu %v", a.st.LastEnd["key:"+game.Key], exitAt)
	}
}

// Scanning again without the game releases its handle.
func TestWatchReleasesClosedGames(t *testing.T) {
	a := testAgent(t)
	cmd := exec.Command("ping", "-n", "30", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	now := time.Now()
	game := candidate{Key: `c:\jeux\jeu.exe`, PID: uint32(cmd.Process.Pid), Started: now, Source: "steam", Names: []string{"Jeu"}}
	a.track([]candidate{game}, now)
	a.watchGames([]candidate{game})
	a.watchGames([]candidate{game}) // no second handle
	if len(a.procs) != 1 {
		t.Fatalf("processus surveillés : %d, attendu 1", len(a.procs))
	}
	a.track(nil, now.Add(time.Minute))
	a.watchGames(nil)
	if len(a.procs) != 0 {
		t.Fatalf("processus surveillés : %d, attendu 0", len(a.procs))
	}
}
