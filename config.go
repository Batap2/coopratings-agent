package main

// App files, all in %LOCALAPPDATA%\CoopRatingsAgent:
//   CoopRatings-Agent.exe  installed copy (started with Windows)
//   config.json            server, username, token (encrypted with Windows DPAPI)
//   state.json             verdicts (game or not), games to identify, active sessions, recent activity
//   queue.json             finished sessions not uploaded yet
//   status.json            summary for the control window
//   inbox\*.json           commands from other processes (tray, notification, control)
//   agent.log              log
//
// Only the main process writes state.json and queue.json. The others (tray
// icon, notification click, control window) drop a command into inbox\ and
// wake the main process.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const defaultServer = "https://coopratings.duckdns.org"

var appDir string // app data folder (see initPaths)

func initPaths(dev bool) error {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		return errors.New("LOCALAPPDATA introuvable")
	}
	name := "CoopRatingsAgent"
	if dev {
		name = "CoopRatingsAgent-dev"
		useDevNames()
	}
	appDir = filepath.Join(base, name)
	return os.MkdirAll(filepath.Join(appDir, "inbox"), 0o755)
}

func appFile(name string) string { return filepath.Join(appDir, name) }

// --- Atomic JSON read/write --------------------------------------------------

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// writeJSON writes to a temporary file then renames it, so a reader never
// sees a half-written file.
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// --- Configuration -----------------------------------------------------------

type config struct {
	Server   string `json:"server"`
	Username string `json:"username,omitempty"`
	Token    string `json:"token,omitempty"` // DPAPI-encrypted token, base64
	// AutostartOff records an explicit autostart opt-out; otherwise the app
	// re-registers itself for autostart on every launch.
	AutostartOff bool `json:"autostartOff,omitempty"`
}

func loadConfig() config {
	var c config
	readJSON(appFile("config.json"), &c)
	if c.Server == "" {
		c.Server = defaultServer
	}
	return c
}

func saveConfig(c config) error { return writeJSON(appFile("config.json"), c) }

// token returns the decrypted token, or "" if absent.
func (c config) token() string {
	if c.Token == "" {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(c.Token)
	if err != nil {
		return ""
	}
	plain, err := dpapi(raw, false)
	if err != nil {
		return ""
	}
	return string(plain)
}

// setToken replaces the token ("" = logout). The previous one is handed,
// still encrypted, to the main process, which revokes it on the server.
func (c *config) setToken(t string) error {
	old := c.Token
	if t == "" {
		c.Token = ""
	} else {
		enc, err := dpapi([]byte(t), true)
		if err != nil {
			return err
		}
		c.Token = base64.StdEncoding.EncodeToString(enc)
	}
	if old != "" && old != c.Token {
		postInbox(inboxMsg{Type: "revoke", Token: old})
	}
	return nil
}

// dpapi encrypts/decrypts with the current Windows user's key: the file is
// useless if copied to another PC or account.
func dpapi(data []byte, encrypt bool) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("vide")
	}
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	var err error
	if encrypt {
		err = windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	} else {
		err = windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	}
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	res := make([]byte, out.Size)
	copy(res, unsafe.Slice(out.Data, out.Size))
	return res, nil
}

// --- State -------------------------------------------------------------------

// verdict is the server's answer for a detection key (an exe path, or an
// emulator + loaded game).
type verdict struct {
	Verdict string    `json:"verdict"` // game | notgame | unknown
	ItemID  int       `json:"itemId,omitempty"`
	IGDBID  int       `json:"igdbId,omitempty"`
	Title   string    `json:"title,omitempty"`
	At      time.Time `json:"at"`
}

// unknownGame is a detected game known to neither the server nor IGDB: the
// "!" tray badge shows until it is identified manually.
type unknownGame struct {
	Key       string    `json:"key"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	FirstSeen time.Time `json:"firstSeen"`
	Seconds   int       `json:"seconds"` // pending playtime
}

// activeSession is a game being played, saved on every tick to survive an
// app restart.
type activeSession struct {
	Identity string    `json:"identity"`
	Key      string    `json:"key"`
	Title    string    `json:"title"`
	Platform string    `json:"platform,omitempty"`
	Source   string    `json:"source"`
	Start    time.Time `json:"start"`
	LastSeen time.Time `json:"lastSeen"`
}

// pendingRevoke is a dropped token (logout, new login) waiting to be revoked
// on the server, kept DPAPI-encrypted.
type pendingRevoke struct {
	Token string    `json:"token"`
	At    time.Time `json:"at"`
}

// pendingIdent is a detected game not yet identified by the server (server
// unreachable…): kept for retry even if the game is closed in the meantime.
type pendingIdent struct {
	Cand apiCandidate `json:"cand"`
	Path string       `json:"path"`
}

type state struct {
	Verdicts     map[string]verdict        `json:"verdicts"`
	Pending      map[string]pendingIdent   `json:"pending"`
	Unknown      map[string]unknownGame    `json:"unknown"`
	Active       map[string]*activeSession `json:"active"`
	Muted        map[int]bool              `json:"muted"`
	PendingMutes []int                     `json:"pendingMutes,omitempty"`
	Revoke       []pendingRevoke           `json:"revoke,omitempty"`
	LastEnd      map[string]time.Time      `json:"lastEnd"`             // end of the last session per game (prevents double counting)
	TrayShown    bool                      `json:"trayShown,omitempty"` // icon put in the taskbar by the app (taskbar.go)
	History      []historyEntry            `json:"history,omitempty"`   // finished sessions, oldest first (history.go)
}

func loadState() *state {
	s := &state{}
	readJSON(appFile("state.json"), s)
	if s.Verdicts == nil {
		s.Verdicts = map[string]verdict{}
	}
	if s.Pending == nil {
		s.Pending = map[string]pendingIdent{}
	}
	if s.Unknown == nil {
		s.Unknown = map[string]unknownGame{}
	}
	if s.Active == nil {
		s.Active = map[string]*activeSession{}
	}
	if s.Muted == nil {
		s.Muted = map[int]bool{}
	}
	if s.LastEnd == nil {
		s.LastEnd = map[string]time.Time{}
	}
	for k, t := range s.LastEnd {
		if time.Since(t) > 48*time.Hour {
			delete(s.LastEnd, k)
		}
	}
	return s
}

func (s *state) save() error { return writeJSON(appFile("state.json"), s) }

// queuedSession is a finished session waiting to be uploaded.
type queuedSession struct {
	ID       string    `json:"id"`
	Key      string    `json:"key"`
	ItemID   int       `json:"itemId,omitempty"`
	IGDBID   int       `json:"igdbId,omitempty"`
	Title    string    `json:"title"`
	Platform string    `json:"platform,omitempty"`
	Source   string    `json:"source"`
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Seconds  int       `json:"seconds"`
	Attempts int       `json:"attempts,omitempty"`
}

func loadQueue() []queuedSession {
	var q []queuedSession
	readJSON(appFile("queue.json"), &q)
	return q
}

func saveQueue(q []queuedSession) error {
	if q == nil {
		q = []queuedSession{}
	}
	return writeJSON(appFile("queue.json"), q)
}

// status is a summary written by the main process for the control window.
type status struct {
	Version  string    `json:"version"`
	Username string    `json:"username"`
	Playing  []string  `json:"playing"`
	Queue    int       `json:"queue"`
	Unknown  int       `json:"unknown"`
	LastSync time.Time `json:"lastSync"`
	Offline  bool      `json:"offline"`
	At       time.Time `json:"at"`
	// Recent activity, newest first.
	Recent     []recentEntry `json:"recent"`
	MinSession int           `json:"minSession"` // seconds
}
