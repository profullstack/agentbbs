package sites

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/profullstack/agentbbs/internal/store"
)

func TestNormalizeAndValid(t *testing.T) {
	cases := []struct {
		in    string
		norm  string
		valid bool
	}{
		{"Chovy.com", "chovy.com", true},
		{"  https://Example.COM/ ", "example.com", true},
		{"sub.example.co.uk.", "sub.example.co.uk", true},
		{"localhost", "localhost", false},           // no TLD
		{"bad_domain.com", "bad_domain.com", false}, // underscore
		{"../etc/passwd", "../etc/passwd", false},
		{"", "", false},
	}
	for _, c := range cases {
		if got := Normalize(c.in); got != c.norm {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.norm)
		}
		if got := Valid(Normalize(c.in)); got != c.valid {
			t.Errorf("Valid(Normalize(%q)) = %v, want %v", c.in, got, c.valid)
		}
	}
}

func TestManagerAddRemoveSyncAsk(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	m, err := NewManager(st, dir)
	if err != nil {
		t.Fatal(err)
	}

	// Add creates the DB row and a symlink to the user's public_html.
	if _, err := m.Add("Chovy.com", "chovy"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	link := filepath.Join(dir, "domains", "chovy.com")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("expected symlink at %s: %v", link, err)
	}
	if want := filepath.Join(dir, "users", "chovy", "public_html"); target != want {
		t.Errorf("symlink target = %q, want %q", target, want)
	}

	// A different user cannot steal a mapped domain.
	if _, err := m.Add("chovy.com", "someoneelse"); err != store.ErrDomainTaken {
		t.Errorf("expected ErrDomainTaken, got %v", err)
	}

	// Invalid domains are rejected.
	if _, err := m.Add("not a domain", "chovy"); err != ErrInvalidDomain {
		t.Errorf("expected ErrInvalidDomain, got %v", err)
	}

	// Ask endpoint: 200 for mapped, 404 for unmapped, 400 for junk.
	h := m.AskHandler()
	for _, c := range []struct {
		domain string
		code   int
	}{
		{"chovy.com", http.StatusOK},
		{"unmapped.com", http.StatusNotFound},
		{"localhost", http.StatusBadRequest},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/check?domain="+c.domain, nil)
		h.ServeHTTP(rec, req)
		if rec.Code != c.code {
			t.Errorf("ask %q = %d, want %d", c.domain, rec.Code, c.code)
		}
	}

	// Sync rebuilds the farm from the DB after the link is removed out-of-band.
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := m.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if _, err := os.Readlink(link); err != nil {
		t.Errorf("Sync did not restore symlink: %v", err)
	}

	// Remove drops both the row and the link.
	if _, err := m.Remove("chovy.com", "chovy"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf("expected symlink gone, got err=%v", err)
	}
	if _, ok, _ := st.DomainUser("chovy.com"); ok {
		t.Error("expected domain unmapped in store")
	}
}

func TestAskUserSubdomain(t *testing.T) {
	t.Setenv("AGENTBBS_HOST", "bbs.profullstack.com")
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.EnsureUser("alice", "member", "SHA256:aaa"); err != nil {
		t.Fatal(err)
	}

	m, err := NewManager(st, dir)
	if err != nil {
		t.Fatal(err)
	}
	h := m.AskHandler()

	for _, c := range []struct {
		domain string
		code   int
	}{
		{"alice.bbs.profullstack.com", http.StatusOK},        // registered member → cert allowed
		{"nobody.bbs.profullstack.com", http.StatusNotFound}, // no such user
		{"a.b.bbs.profullstack.com", http.StatusNotFound},    // multi-label, not a user subdomain
		{"bbs.profullstack.com", http.StatusNotFound},        // apex is not a user subdomain
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/check?domain="+c.domain, nil)
		h.ServeHTTP(rec, req)
		if rec.Code != c.code {
			t.Errorf("ask %q = %d, want %d", c.domain, rec.Code, c.code)
		}
	}
}
