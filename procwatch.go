package main

// Spotting that a game has closed without polling: the app keeps a handle on
// each running game's process and waits on these handles in the kernel, along
// with the wake event (WaitForMultipleObjects). When a game closes, the next
// scan runs exitDelay later instead of up to a minute later, so the session
// is uploaded and the "rate it?" notification shows ~10 s after the game
// closed. The delay lets a game that relaunches itself be seen again before
// its session ends.

import (
	"log"
	"time"

	"golang.org/x/sys/windows"
)

var exitDelay = 10 * time.Second // variable for the tests

// maxWatched: WaitForMultipleObjects takes at most 64 handles, one of which is
// the wake event.
const maxWatched = 63

type watchedProc struct {
	h  windows.Handle
	id string // session identity (activeSession.Identity)
}

// watchGames holds a handle on every process of an active session and
// releases the others.
func (a *agent) watchGames(cands []candidate) {
	if a.procs == nil {
		a.procs = map[uint32]watchedProc{}
	}
	keep := map[uint32]bool{}
	for _, c := range cands {
		id, _ := a.identityOf(c)
		if c.PID == 0 || id == "" || a.st.Active[id] == nil {
			continue
		}
		keep[c.PID] = true
		if p, ok := a.procs[c.PID]; ok {
			p.id = id // identified since: same session under its real identity
			a.procs[c.PID] = p
			continue
		}
		if len(a.procs) >= maxWatched {
			continue
		}
		// While the handle is open, Windows does not reuse the PID.
		h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, c.PID)
		if err != nil {
			continue // the minute scan will still see it close
		}
		a.procs[c.PID] = watchedProc{h: h, id: id}
	}
	for pid, p := range a.procs {
		if !keep[pid] {
			windows.CloseHandle(p.h)
			delete(a.procs, pid)
		}
	}
}

// wait sleeps until the next scan: after the interval, as soon as another
// process wakes the app (inbox), or exitDelay after a watched game closes.
// The thread is blocked in the kernel the whole time: nothing is polled.
func (a *agent) wait() {
	deadline := time.Now().Add(a.interval)
	for {
		d := time.Until(deadline)
		if d <= 0 {
			return
		}
		var handles []windows.Handle
		if a.wake != 0 {
			handles = append(handles, a.wake)
		}
		first := len(handles) // index of the first process handle
		var pids []uint32
		for pid, p := range a.procs {
			handles = append(handles, p.h)
			pids = append(pids, pid)
		}
		if len(handles) == 0 {
			time.Sleep(d)
			return
		}
		ms := uint32((d + time.Millisecond - 1) / time.Millisecond)
		ev, err := windows.WaitForMultipleObjects(handles, false, ms)
		// Timeout, wake event or error: scan now.
		if err != nil || int(ev) < first || int(ev) >= len(handles) {
			return
		}
		now := time.Now()
		a.gameExited(pids[int(ev)-first], now)
		if t := now.Add(exitDelay); t.Before(deadline) {
			deadline = t
		}
	}
}

// gameExited: a watched process has ended. Its exit time becomes the
// session's last observation (more exact than the last scan); the next scan
// ends the session unless another process of the game is still running.
func (a *agent) gameExited(pid uint32, now time.Time) {
	p := a.procs[pid]
	windows.CloseHandle(p.h) // signalled for good: must leave the wait list
	delete(a.procs, pid)
	if s := a.st.Active[p.id]; s != nil && now.After(s.LastSeen) {
		s.LastSeen = now
		log.Printf("fermé : %s", s.Title)
	}
}
