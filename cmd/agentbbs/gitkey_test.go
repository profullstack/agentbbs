package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/profullstack/agentbbs/internal/forgejo"
	"github.com/profullstack/agentbbs/internal/store"
)

// Throwaway keys generated for these tests only.
const testPubKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOeWE4BpdSRsfc8l6w9clDKPTDH9GX/oYSgtxM3ohyhV chovy@bbs"

func TestGitKeyTitleIsPerKey(t *testing.T) {
	const other = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHHfPGCIu0pk6TrpwdX9VBrGbQXUU44L5ovCRqFFsWSo chovy@bbs"

	a := gitKeyTitle(testPubKey)
	if !strings.HasPrefix(a, "agentbbs ") {
		t.Errorf("title = %q, want an \"agentbbs \" prefix", a)
	}
	// Two different keys must not collide on title: Forgejo 422s a duplicate
	// title, which would silently drop the member's rotated key.
	if b := gitKeyTitle(other); a == b {
		t.Errorf("distinct keys share the title %q", a)
	}
	// The same key is stable across logins, so we don't pile up entries.
	if a != gitKeyTitle(testPubKey) {
		t.Error("gitKeyTitle is not stable for the same key")
	}
	// Unparseable input still yields a usable title rather than panicking.
	if got := gitKeyTitle("not-a-key"); got != "agentbbs" {
		t.Errorf("gitKeyTitle(garbage) = %q, want \"agentbbs\"", got)
	}
}

// TestProvisionGitRegistersKeyForExistingAccount is the regression guard for the
// bug that lost chovy's key: provisionGit used to return early when the Forgejo
// account already existed, so EnsureKey ran only at first provisioning and a key
// deleted on AgentGit never came back on any later BBS login.
func TestProvisionGitRegistersKeyForExistingAccount(t *testing.T) {
	var posted map[string]any
	createAttempted := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		// Account already exists — this is the case that used to bail out.
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/users/chovy":
			_, _ = w.Write([]byte(`{"id":1,"login":"chovy"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/users/chovy/keys":
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/admin/users/chovy/keys":
			_ = json.NewDecoder(r.Body).Decode(&posted)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":7}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/admin/users":
			createAttempted = true
			w.WriteHeader(http.StatusUnprocessableEntity)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	a := &app{forgejo: forgejo.Config{BaseURL: srv.URL, Token: "secret"}}
	u := &store.User{ID: 1, Name: "chovy", Email: "chovy@example.com", EmailVerified: true}

	a.provisionGit(u, testPubKey)

	if createAttempted {
		t.Error("must not re-create an account that already exists")
	}
	if posted == nil {
		t.Fatal("key was never POSTed for an existing account — the early return is back")
	}
	if posted["key"] != testPubKey {
		t.Errorf("posted key = %v, want %q", posted["key"], testPubKey)
	}
}
