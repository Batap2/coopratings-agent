package main

// Auto-update from the releases published on GitHub, built by
// .github/workflows/release.yml from the repository source.
//
// Shortly after startup and then once a day: if a newer version exists, the
// exe is downloaded in the background and its SHA-256 checksum verified
// (.sha256 file published alongside it). It replaces the installed exe as
// soon as no game is running, then the app restarts: sessions continue
// (restoreActive), login and settings are kept.
//
// A running exe cannot be overwritten but can be renamed: the old one becomes
// CoopRatings-Agent.old.exe, deleted on the next launch.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	updateRepo      = "Batap2/coopratings-agent"
	updateAsset     = "CoopRatings-Agent.exe"
	updateEvery     = 24 * time.Hour
	updateRetry     = 6 * time.Hour // after a failure (no network…)
	updateMaxSize   = 64 << 20
	afterUpdateFlag = "--after-update"
)

var (
	releasesURL = "https://api.github.com/repos/" + updateRepo + "/releases/latest"
	updateFirst = 2 * time.Minute // let Windows finish starting up
	updateTest  bool              // end-to-end test, see init
)

// End-to-end test in development (--dev) against a local fake GitHub
// (COOPRATINGS_AGENT_UPDATE_URL = URL of "releases/latest").
func init() {
	if u := os.Getenv("COOPRATINGS_AGENT_UPDATE_URL"); u != "" {
		releasesURL, updateFirst, updateTest = u, 3*time.Second, true
	}
}

type ghRelease struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

// parseVersion parses "v1.2.3" or "1.2.3" into [1 2 3]; ok is false for "dev" and others.
func parseVersion(s string) ([3]int, bool) {
	var v [3]int
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(s), "v"), ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

// isNewer reports whether latest is strictly newer than current. An
// unparsable version (dev build) is never updated.
func isNewer(latest, current string) bool {
	l, ok1 := parseVersion(latest)
	c, ok2 := parseVersion(current)
	if !ok1 || !ok2 {
		return false
	}
	for i := range l {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

// updateReady is a downloaded and verified new version, ready to install.
type updateReady struct {
	Version string
	Path    string
}

func updateHTTP() *http.Client { return &http.Client{Timeout: 5 * time.Minute} }

func ghGet(client *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "CoopRatings-Agent/"+version)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("%s : %s", url, resp.Status)
	}
	return resp, nil
}

// checkUpdate returns the downloaded update, or nil if already up to date.
func checkUpdate() (*updateReady, error) {
	client := updateHTTP()
	resp, err := ghGet(client, releasesURL)
	if err != nil {
		return nil, err
	}
	var rel ghRelease
	err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if rel.Draft || rel.Prerelease || !isNewer(rel.Tag, version) {
		return nil, nil
	}
	var exeURL, sumURL string
	for _, a := range rel.Assets {
		switch a.Name {
		case updateAsset:
			exeURL = a.URL
		case updateAsset + ".sha256":
			sumURL = a.URL
		}
	}
	if exeURL == "" || sumURL == "" {
		return nil, fmt.Errorf("version %s incomplète (exe ou empreinte manquant)", rel.Tag)
	}

	// Expected checksum ("HASH" or "HASH  name").
	resp, err = ghGet(client, sumURL)
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 || len(fields[0]) != 64 {
		return nil, errors.New("empreinte SHA-256 illisible")
	}
	want := strings.ToLower(fields[0])

	resp, err = ghGet(client, exeURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	path := appFile("update.exe")
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, updateMaxSize+1))
	f.Close()
	if err == nil && n > updateMaxSize {
		err = errors.New("fichier trop gros")
	}
	if err == nil && hex.EncodeToString(h.Sum(nil)) != want {
		err = errors.New("empreinte SHA-256 différente : fichier abîmé ou modifié")
	}
	if err != nil {
		os.Remove(path)
		return nil, err
	}
	return &updateReady{Version: strings.TrimPrefix(rel.Tag, "v"), Path: path}, nil
}

// installUpdate replaces the installed exe with the new version. On success
// the caller must exit and then call startUpdated.
func installUpdate(u *updateReady) error {
	exe, old := installedExe(), oldExe()
	os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return err
	}
	if err := os.Rename(u.Path, exe); err != nil {
		os.Rename(old, exe) // restore the old version
		return err
	}
	return nil
}

// startUpdated starts the new version (same options, no welcome window),
// which waits for this process to exit. On failure the old version is
// restored and relaunched.
func startUpdated() {
	args := []string{"--autostart", afterUpdateFlag}
	for _, a := range os.Args[1:] {
		if a != "--autostart" && a != afterUpdateFlag {
			args = append(args, a)
		}
	}
	err := exec.Command(installedExe(), args...).Start()
	if err == nil {
		return
	}
	log.Printf("mise à jour : lancement impossible (%v), retour à l'ancienne version", err)
	os.Remove(installedExe())
	if os.Rename(oldExe(), installedExe()) == nil {
		exec.Command(installedExe(), args...).Start()
	}
}

func oldExe() string { return appFile("CoopRatings-Agent.old.exe") }

// cleanupUpdate removes leftovers from a previous update.
func cleanupUpdate() {
	os.Remove(oldExe())
	os.Remove(appFile("update.exe"))
}
