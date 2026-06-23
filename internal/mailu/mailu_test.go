package mailu

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConfigured(t *testing.T) {
	if New(Config{BaseURL: "http://x"}).Configured() {
		t.Fatal("no token should be unconfigured")
	}
	if !New(Config{BaseURL: "http://x", Token: "tok"}).Configured() {
		t.Fatal("token should be configured")
	}
	var nilc *Client
	if nilc.Configured() {
		t.Fatal("nil client must be unconfigured")
	}
}

func TestEnsureUserCreatesWhenMissing(t *testing.T) {
	var created map[string]any
	var sawToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawToken = r.Header.Get("Authorization")
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/user/"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/user":
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &created)
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Token: "secret-tok"})
	if err := c.EnsureUser(context.Background(), "alice", "bbs.profullstack.com"); err != nil {
		t.Fatal(err)
	}
	if sawToken != "secret-tok" {
		t.Fatalf("token header = %q", sawToken)
	}
	if created["email"] != "alice@bbs.profullstack.com" {
		t.Fatalf("created email = %v", created["email"])
	}
	if created["raw_password"] == nil || created["raw_password"] == "" {
		t.Fatal("expected a generated password")
	}
}

func TestEnsureUserIdempotentWhenExists(t *testing.T) {
	posted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		posted = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Token: "t"})
	if err := c.EnsureUser(context.Background(), "bob", "bbs.profullstack.com"); err != nil {
		t.Fatal(err)
	}
	if posted {
		t.Fatal("should not POST when the mailbox already exists")
	}
}

func TestEnsureUserConflictIsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"message":"already exists"}`)
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, Token: "t"})
	if err := c.EnsureUser(context.Background(), "carol", "bbs.profullstack.com"); err != nil {
		t.Fatalf("conflict should be treated as success, got %v", err)
	}
}

func TestEnsureUserUnconfigured(t *testing.T) {
	if err := New(Config{}).EnsureUser(context.Background(), "x", "y"); err == nil {
		t.Fatal("expected error when unconfigured")
	}
}
