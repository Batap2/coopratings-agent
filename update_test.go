package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestIsNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"v0.1.1", "0.1.0", true},
		{"v0.2.0", "0.1.9", true},
		{"v1.0.0", "0.9.9", true},
		{"v0.10.0", "0.9.0", true}, // not a lexical comparison
		{"v0.1.0", "0.1.0", false},
		{"v0.1.0", "0.2.0", false}, // never downgrade
		{"v0.2.0", "dev", false},   // dev build: no updates
		{"nightly", "0.1.0", false},
	}
	for _, c := range cases {
		if got := isNewer(c.latest, c.current); got != c.want {
			t.Errorf("isNewer(%q, %q) = %v, attendu %v", c.latest, c.current, got, c.want)
		}
	}
}

// fakeGitHub serves a release with its exe and SHA-256 checksum, in the
// release workflow's format (uppercase hex + Windows line ending).
func fakeGitHub(t *testing.T, tag string, exe []byte, sum string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			json.NewEncoder(w).Encode(map[string]any{
				"tag_name": tag,
				"assets": []map[string]any{
					{"name": updateAsset, "browser_download_url": srv.URL + "/exe"},
					{"name": updateAsset + ".sha256", "browser_download_url": srv.URL + "/sum"},
				},
			})
		case "/exe":
			w.Write(exe)
		case "/sum":
			w.Write([]byte(sum + "\r\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := releasesURL
	releasesURL = srv.URL + "/latest"
	t.Cleanup(func() { releasesURL = old })
	return srv
}

func withVersion(t *testing.T, v string) {
	old := version
	version = v
	t.Cleanup(func() { version = old })
}

func TestCheckUpdateDownloadsAndVerifies(t *testing.T) {
	appDir = t.TempDir()
	withVersion(t, "0.1.0")
	exe := []byte("MZ nouvelle version")
	h := sha256.Sum256(exe)
	fakeGitHub(t, "v0.2.0", exe, strings.ToUpper(hex.EncodeToString(h[:])))

	u, err := checkUpdate()
	if err != nil || u == nil {
		t.Fatalf("checkUpdate : %v, %v", u, err)
	}
	if u.Version != "0.2.0" {
		t.Errorf("version %q", u.Version)
	}
	if b, _ := os.ReadFile(u.Path); string(b) != string(exe) {
		t.Errorf("fichier téléchargé incorrect : %q", b)
	}
}

func TestCheckUpdateRejectsBadChecksum(t *testing.T) {
	appDir = t.TempDir()
	withVersion(t, "0.1.0")
	fakeGitHub(t, "v0.2.0", []byte("MZ modifié"), strings.Repeat("ab", 32))

	if u, err := checkUpdate(); err == nil || u != nil {
		t.Fatalf("une empreinte fausse doit être refusée : %v, %v", u, err)
	}
	if _, err := os.Stat(appFile("update.exe")); !os.IsNotExist(err) {
		t.Error("le fichier refusé doit être supprimé")
	}
}

func TestCheckUpdateUpToDate(t *testing.T) {
	appDir = t.TempDir()
	withVersion(t, "0.2.0")
	fakeGitHub(t, "v0.2.0", []byte("MZ"), strings.Repeat("ab", 32))
	if u, err := checkUpdate(); err != nil || u != nil {
		t.Fatalf("déjà à jour : %v, %v", u, err)
	}
}

func TestInstallUpdateSwapsExe(t *testing.T) {
	appDir = t.TempDir()
	os.WriteFile(installedExe(), []byte("ancienne"), 0o755)
	os.WriteFile(appFile("update.exe"), []byte("nouvelle"), 0o755)

	if err := installUpdate(&updateReady{Version: "0.2.0", Path: appFile("update.exe")}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(installedExe()); string(b) != "nouvelle" {
		t.Errorf("exe installé : %q", b)
	}
	if b, _ := os.ReadFile(oldExe()); string(b) != "ancienne" {
		t.Errorf("ancienne version gardée : %q", b)
	}
	cleanupUpdate()
	if _, err := os.Stat(oldExe()); !os.IsNotExist(err) {
		t.Error("l'ancienne version doit être supprimée au lancement suivant")
	}
}
