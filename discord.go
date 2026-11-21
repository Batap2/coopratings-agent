package main

// List of known game executables, the one Discord uses for "Playing ...",
// provided by the server (GET /api/agent/detectable) and cached for a week in
// detectable.json. An entry is either a bare exe name ("hades.exe") or
// folder + exe ("diablo ii/game.exe") for generic names.

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

type detectableList struct {
	byExe    map[string]string   // "hades.exe" -> "Hades"
	bySuffix map[string][]string // "game.exe" -> ["diablo ii/game.exe", ...]
	names    map[string]string   // full entry -> name
	loadedAt time.Time
}

// genericExe lists exe names too generic to be recognised without their folder.
var genericExe = map[string]bool{
	"game.exe": true, "launcher.exe": true, "start.exe": true, "play.exe": true, "main.exe": true,
	"app.exe": true, "client.exe": true, "run.exe": true, "setup.exe": true, "update.exe": true,
	"chrome.exe": true, "code.exe": true, "java.exe": true, "javaw.exe": true, "python.exe": true,
	"nw.exe": true, "electron.exe": true, "unity.exe": true, "godot.exe": true,
}

func loadDetectable() *detectableList {
	var raw struct {
		Updated string      `json:"updated"`
		Games   [][2]string `json:"games"`
	}
	d := &detectableList{byExe: map[string]string{}, bySuffix: map[string][]string{}, names: map[string]string{}}
	path := appFile("detectable.json")
	if readJSON(path, &raw) != nil {
		return d
	}
	if st, err := os.Stat(path); err == nil {
		d.loadedAt = st.ModTime()
	}
	for _, g := range raw.Games {
		entry, name := strings.ToLower(g[0]), g[1]
		if strings.Contains(entry, "/") {
			base := entry[strings.LastIndex(entry, "/")+1:]
			d.bySuffix[base] = append(d.bySuffix[base], entry)
			d.names[entry] = name
		} else if !genericExe[entry] {
			d.byExe[entry] = name
		}
	}
	return d
}

func (d *detectableList) stale() bool {
	return len(d.names)+len(d.byExe) == 0 || time.Since(d.loadedAt) > 7*24*time.Hour
}

// match returns the game name if the exe is in the list. withFolder is true
// if the match includes the folder (more reliable).
func (d *detectableList) match(exePath string) (name string, withFolder bool) {
	p := strings.ToLower(filepath.ToSlash(exePath))
	base := filepath.Base(p)
	for _, entry := range d.bySuffix[base] {
		if strings.HasSuffix(p, "/"+entry) {
			return d.names[entry], true
		}
	}
	if n, ok := d.byExe[base]; ok {
		return n, false
	}
	return "", false
}
