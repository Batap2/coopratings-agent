package main

// Client for the CoopRatings API (server side: agent_api.go).

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

var errUnauthorized = errors.New("session expirée")

// httpError is an error answered by the server (status >= 300), as opposed
// to a network error.
type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

type apiClient struct {
	server string
	token  string
	http   *http.Client
}

func newAPI(c config) *apiClient {
	// Generous timeout: the server may import a game from IGDB during the call.
	return &apiClient{server: strings.TrimRight(c.Server, "/"), token: c.token(), http: &http.Client{Timeout: 90 * time.Second}}
}

func (a *apiClient) do(method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, a.server+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "CoopRatings-Agent/"+version)
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized && path != "/api/agent/login" {
		return errUnauthorized
	}
	if serverLacksAPI(resp) {
		return fmt.Errorf("le site %s n'est pas encore à jour pour l'appli", a.server)
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return &httpError{status: resp.StatusCode, msg: e.Error}
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// serverLacksAPI reports whether the response comes from a server that does
// not have the agent API yet (older than the app). The API always answers
// JSON; without it, the site serves its HTML page for unknown URLs (200),
// "404 page not found" or "405 Method Not Allowed". Gateway errors (502…)
// mean the server is down, not outdated, and are left alone.
func serverLacksAPI(resp *http.Response) bool {
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		return false
	}
	switch resp.StatusCode {
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return true
	}
	return resp.StatusCode < 300
}

// login sends the site login and password only: nothing about the PC (not
// even its name).
func (a *apiClient) login(login, password string) (token, username string, err error) {
	var out struct {
		Token    string `json:"token"`
		Username string `json:"username"`
	}
	err = a.do("POST", "/api/agent/login", map[string]string{"login": login, "password": password}, &out)
	return out.Token, out.Username, err
}

// apiCandidate is a program to identify. Locally, Key is the exe path; the
// copy sent to the server has a plain number instead (agent.identify), so no
// path ever leaves the PC.
type apiCandidate struct {
	Key        string   `json:"key"`
	SteamAppID int      `json:"steamAppId,omitempty"`
	Names      []string `json:"names"`
	Platforms  []string `json:"platforms,omitempty"`
}

type apiVerdict struct {
	Key     string `json:"key"`
	Verdict string `json:"verdict"`
	ItemID  int    `json:"itemId"`
	IGDBID  int    `json:"igdbId"`
	Title   string `json:"title"`
	Reason  string `json:"reason"`
}

func (a *apiClient) identify(c []apiCandidate) ([]apiVerdict, error) {
	var out struct {
		Verdicts []apiVerdict `json:"verdicts"`
	}
	err := a.do("POST", "/api/agent/identify", map[string]any{"candidates": c}, &out)
	return out.Verdicts, err
}

type apiSearchHit struct {
	ItemID    int      `json:"itemId"`
	IGDBID    int      `json:"igdbId"`
	Title     string   `json:"title"`
	Year      int      `json:"year"`
	Detail    string   `json:"detail"`
	Type      string   `json:"type"`      // "Jeu principal" (main game), DLC, Extension, Pack…
	Platforms []string `json:"platforms"` // PC, PS5, Switch…
}

func (a *apiClient) search(q string) ([]apiSearchHit, error) {
	var out struct {
		Results []apiSearchHit `json:"results"`
	}
	err := a.do("GET", "/api/agent/search?q="+url.QueryEscape(q), nil, &out)
	return out.Results, err
}

func (a *apiClient) resolve(itemID, igdbID int, title string) (int, string, error) {
	var out struct {
		ItemID int    `json:"itemId"`
		Title  string `json:"title"`
	}
	err := a.do("POST", "/api/agent/resolve", map[string]any{"itemId": itemID, "igdbId": igdbID, "title": title}, &out)
	return out.ItemID, out.Title, err
}

// apiSession is a finished session: a game, the day it started and its
// duration. The time of day never leaves the PC.
type apiSession struct {
	ID       string `json:"id"` // random, so that a resend is not counted twice
	ItemID   int    `json:"itemId,omitempty"`
	IGDBID   int    `json:"igdbId,omitempty"`
	Platform string `json:"platform,omitempty"`
	Source   string `json:"source"`
	Day      string `json:"day"` // "2026-11-20", PC local date of the start
	Seconds  int    `json:"seconds"`
}

// newAPISession converts a queued session: of its start, only the date is
// sent.
func newAPISession(q queuedSession) apiSession {
	return apiSession{ID: q.ID, ItemID: q.ItemID, IGDBID: q.IGDBID, Platform: q.Platform, Source: q.Source,
		Day: q.Start.Format("2006-01-02"), Seconds: q.Seconds}
}

type apiSessionResult struct {
	ID      string `json:"id"`
	OK      bool   `json:"ok"`
	ItemID  int    `json:"itemId"`
	Title   string `json:"title"`
	Added   bool   `json:"added"`   // the game was not in the list before
	Ignored bool   `json:"ignored"` // game removed from the list by the user
	Notify  bool   `json:"notify"`
	URL     string `json:"url"`
	Note    string `json:"note"`
	Error   string `json:"error"`
}

func (a *apiClient) sendSessions(s []apiSession) ([]apiSessionResult, error) {
	var out struct {
		Results []apiSessionResult `json:"results"`
	}
	err := a.do("POST", "/api/agent/sessions", map[string]any{"sessions": s}, &out)
	return out.Results, err
}

// tokenLifetime: the server refuses a token one year after login.
const tokenLifetime = 365 * 24 * time.Hour

// revokeToken revokes a DPAPI-encrypted token on the server.
func revokeToken(server, enc string) error {
	t := config{Token: enc}.token()
	if t == "" {
		return nil // nothing (readable) to revoke
	}
	api := &apiClient{server: strings.TrimRight(server, "/"), token: t, http: &http.Client{Timeout: 10 * time.Second}}
	return api.do("POST", "/api/agent/logout", nil, nil)
}

func (a *apiClient) mute(itemID int) error {
	return a.do("POST", "/api/agent/mute", map[string]int{"itemId": itemID}, nil)
}

// downloadDetectable saves the list of known game executables to dest.
func (a *apiClient) downloadDetectable(dest string) error {
	var raw json.RawMessage
	if err := a.do("GET", "/api/agent/detectable", nil, &raw); err != nil {
		return err
	}
	if len(raw) < 1000 {
		return fmt.Errorf("liste vide")
	}
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

// apiGame is a game as it appears in the account's list (rating window).
type apiGame struct {
	ItemID int     `json:"itemId"`
	Title  string  `json:"title"`
	Year   int     `json:"year"`
	Grade  *int    `json:"grade"`
	Status *int    `json:"status"` // -2 not finishable, -1 not started, 0 not finished, N finished N times
	URL    string  `json:"url"`
	Hours  float64 `json:"hours"`
}

func (a *apiClient) game(itemID int) (*apiGame, error) {
	var out apiGame
	err := a.do("GET", fmt.Sprintf("/api/agent/game?item=%d", itemID), nil, &out)
	return &out, err
}

// rate saves the grade (0..20) and/or status; nil leaves a field unchanged.
func (a *apiClient) rate(itemID int, grade, status *int) (*apiGame, error) {
	var out apiGame
	err := a.do("POST", "/api/agent/rate", map[string]any{"itemId": itemID, "grade": grade, "status": status}, &out)
	return &out, err
}
