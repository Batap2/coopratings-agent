package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A server without the agent API must give a clear "site not up to date"
// error, whatever it answers; real API errors and outages must not.
func TestServerWithoutAgentAPI(t *testing.T) {
	const outdated = "pas encore à jour"
	cases := []struct {
		name    string
		handler http.HandlerFunc
		method  string
		want    string
	}{
		{"HTML page for an unknown URL", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte("<!DOCTYPE html><html>Liste des jeux</html>"))
		}, "GET", outdated},
		{"405 on POST", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}, "POST", outdated},
		{"plain 404", http.NotFound, "GET", outdated},
		{"API error in JSON", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"jeu introuvable"}`))
		}, "GET", "jeu introuvable"},
		{"server down behind the proxy", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusBadGateway)
		}, "GET", "502"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(c.handler)
			defer srv.Close()
			var out map[string]any
			err := (&apiClient{server: srv.URL, http: srv.Client()}).do(c.method, "/api/agent/me", nil, &out)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("erreur %v, attendu %q", err, c.want)
			}
		})
	}
}
