package main

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"
)

// A new login or a logout hands the previous token to the main process, which
// revokes it on the server, retrying while the server is unreachable.
func TestDroppedTokensAreRevoked(t *testing.T) {
	var revoked []string
	up := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		if r.Method == "POST" && r.URL.Path == "/api/agent/logout" {
			revoked = append(revoked, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	a := testAgent(t)
	a.cfg = config{Server: srv.URL}
	cfg := config{Server: srv.URL}
	for _, tok := range []string{"jeton-1", "jeton-2", ""} { // login, new login, logout
		if err := cfg.setToken(tok); err != nil {
			t.Fatal(err)
		}
	}
	a.handleInbox(time.Now())
	if len(a.st.Revoke) != 2 {
		t.Fatalf("%d jeton(s) à révoquer, attendu 2", len(a.st.Revoke))
	}

	a.flushRevokes(time.Now())
	if len(a.st.Revoke) != 2 || len(revoked) != 0 {
		t.Fatalf("site injoignable : %d en attente, %d révoqué(s)", len(a.st.Revoke), len(revoked))
	}

	up = true
	a.flushRevokes(time.Now())
	sort.Strings(revoked)
	if len(a.st.Revoke) != 0 || len(revoked) != 2 || revoked[0] != "Bearer jeton-1" || revoked[1] != "Bearer jeton-2" {
		t.Fatalf("en attente %d, révoqués %v", len(a.st.Revoke), revoked)
	}

	// A token past its lifetime has expired on the server: dropped silently.
	old := config{}
	old.setToken("jeton-3")
	a.st.Revoke = []pendingRevoke{{Token: old.Token, At: time.Now().Add(-tokenLifetime - time.Hour)}}
	a.flushRevokes(time.Now())
	if len(a.st.Revoke) != 0 || len(revoked) != 2 {
		t.Fatalf("jeton expiré : en attente %d, révoqués %v", len(a.st.Revoke), revoked)
	}
}
