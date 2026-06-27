package forgejo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConfiguredRequiresURLAndToken(t *testing.T) {
	if (Config{BaseURL: "https://git.example.com"}).Configured() {
		t.Fatal("missing token should be unconfigured")
	}
	if (Config{Token: "t"}).Configured() {
		t.Fatal("missing URL should be unconfigured")
	}
	if !(Config{BaseURL: "https://git.example.com", Token: "t"}).Configured() {
		t.Fatal("URL+token should be configured")
	}
}

func TestEnsureUserNoOpWhenUnconfigured(t *testing.T) {
	if _, _, err := (Config{}).EnsureUser("alice", "a@x.com"); err == nil {
		t.Fatal("expected error when unconfigured")
	}
}

func TestEnsureUserCreatesWhenMissing(t *testing.T) {
	var got struct {
		lookup bool
		create bool
		body   map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/users/alice":
			got.lookup = true
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/admin/users":
			got.create = true
			if r.Header.Get("Authorization") != "token secret" {
				t.Errorf("missing auth header: %q", r.Header.Get("Authorization"))
			}
			_ = json.NewDecoder(r.Body).Decode(&got.body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	defer srv.Close()

	c := Config{BaseURL: srv.URL, Token: "secret"}
	created, password, err := c.EnsureUser("alice", "a@x.com")
	if err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	if !created {
		t.Fatal("expected created=true")
	}
	if password == "" {
		t.Fatal("expected a generated temporary password for a new account")
	}
	if password != got.body["password"] {
		t.Errorf("returned password %q does not match the one sent to forgejo %v", password, got.body["password"])
	}
	if !got.lookup || !got.create {
		t.Fatalf("expected lookup+create, got %+v", got)
	}
	if got.body["must_change_password"] != true {
		t.Errorf("expected must_change_password=true, got %v", got.body["must_change_password"])
	}
	if got.body["username"] != "alice" {
		t.Errorf("expected username alice, got %v", got.body["username"])
	}
}

func TestEnsureUserResetCreatesWhenMissing(t *testing.T) {
	var posted bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/users/alice":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/admin/users":
			posted = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	defer srv.Close()

	c := Config{BaseURL: srv.URL, Token: "secret"}
	created, password, err := c.EnsureUserReset("alice", "a@x.com")
	if err != nil {
		t.Fatalf("EnsureUserReset: %v", err)
	}
	if !created || password == "" || !posted {
		t.Fatalf("expected created+password+POST, got created=%v pw=%q posted=%v", created, password, posted)
	}
}

func TestEnsureUserResetPatchesWhenExists(t *testing.T) {
	var patched bool
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/users/alice":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":1}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/admin/users/alice":
			patched = true
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":1}`))
		case r.Method == http.MethodPost:
			t.Error("must not create when the account already exists")
			w.WriteHeader(http.StatusTeapot)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	defer srv.Close()

	c := Config{BaseURL: srv.URL, Token: "secret"}
	created, password, err := c.EnsureUserReset("alice", "a@x.com")
	if err != nil {
		t.Fatalf("EnsureUserReset: %v", err)
	}
	if created {
		t.Fatal("expected created=false for existing account")
	}
	if password == "" {
		t.Fatal("expected a fresh password even for an existing account")
	}
	if !patched {
		t.Fatal("expected a PATCH to reset the password")
	}
	if body["password"] != password {
		t.Errorf("returned password %q does not match the one sent %v", password, body["password"])
	}
	if body["must_change_password"] != true {
		t.Errorf("expected must_change_password=true, got %v", body["must_change_password"])
	}
}

func TestSetPasswordPatchesChosenPassword(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/users/alice":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":1}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/admin/users/alice":
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":1}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	defer srv.Close()

	c := Config{BaseURL: srv.URL, Token: "secret"}
	if err := c.SetPassword("alice", "member-chosen-pw"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	if body["password"] != "member-chosen-pw" {
		t.Errorf("sent password %v, want member-chosen-pw", body["password"])
	}
	// They chose it, so don't force another change on next sign-in.
	if body["must_change_password"] != false {
		t.Errorf("expected must_change_password=false, got %v", body["must_change_password"])
	}
}

func TestSetPasswordErrorsWhenMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		t.Errorf("must not PATCH a non-existent user (%s %s)", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()

	c := Config{BaseURL: srv.URL, Token: "secret"}
	if err := c.SetPassword("ghost", "pw"); err == nil {
		t.Fatal("expected an error when the account does not exist")
	}
}

func TestSetPasswordUnconfigured(t *testing.T) {
	if err := (Config{}).SetPassword("alice", "pw"); err == nil {
		t.Fatal("expected an error when Forgejo is not configured")
	}
}

func TestEnsureUserNoOpWhenExists(t *testing.T) {
	created := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			created = true
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()

	c := Config{BaseURL: srv.URL, Token: "secret"}
	got, password, err := c.EnsureUser("alice", "a@x.com")
	if err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	if got {
		t.Fatal("expected created=false for existing user")
	}
	if password != "" {
		t.Fatal("expected empty password when account already exists")
	}
	if created {
		t.Fatal("must not POST when the user already exists")
	}
}

const aliceKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITESTKEY alice@bbs"

func TestEnsureKeyAddsWhenMissing(t *testing.T) {
	var posted map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/users/alice/keys":
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/admin/users/alice/keys":
			_ = json.NewDecoder(r.Body).Decode(&posted)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := Config{BaseURL: srv.URL, Token: "secret"}
	added, err := c.EnsureKey("alice", "agentbbs", aliceKey)
	if err != nil {
		t.Fatalf("EnsureKey: %v", err)
	}
	if !added {
		t.Fatal("expected added=true")
	}
	if posted["key"] != aliceKey {
		t.Errorf("posted key = %v", posted["key"])
	}
}

func TestEnsureKeyIdempotentIgnoringComment(t *testing.T) {
	posted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posted = true
		}
		// Same key material, different comment — must be treated as already present.
		_, _ = w.Write([]byte(`[{"key":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITESTKEY different-comment"}]`))
	}))
	defer srv.Close()

	c := Config{BaseURL: srv.URL, Token: "secret"}
	added, err := c.EnsureKey("alice", "agentbbs", aliceKey)
	if err != nil {
		t.Fatalf("EnsureKey: %v", err)
	}
	if added {
		t.Fatal("expected added=false when key material already present")
	}
	if posted {
		t.Fatal("must not POST when the key already exists")
	}
}

func TestEnsureKeyBlankIsNoOp(t *testing.T) {
	c := Config{BaseURL: "https://git.example.com", Token: "t"}
	if added, err := c.EnsureKey("alice", "agentbbs", "  "); err != nil || added {
		t.Fatalf("blank key should be a silent no-op, got added=%v err=%v", added, err)
	}
}
