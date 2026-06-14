package news

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"

	nntpclient "github.com/dustin/go-nntp/client"
	"github.com/profullstack/agentbbs/internal/store"
)

// startServer brings up a news Server over an ephemeral loopback listener and
// returns its address. The server stops when the test ends.
func startServer(t *testing.T) (string, store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ns := New(st, "news.test")
	if err := ns.SeedGroups(nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = ns.Serve(ctx, ln) }()
	return ln.Addr().String(), st
}

func TestServerEndToEnd(t *testing.T) {
	addr, st := startServer(t)
	if _, err := st.EnsureUser("alice", "member", "SHA256:a"); err != nil {
		t.Fatalf("ensure user: %v", err)
	}

	// A non-member cannot authenticate.
	if _, err := Dial(addr, "intruder"); err == nil {
		t.Fatal("non-member should not be able to connect")
	}

	// An unauthenticated raw client must not be able to LIST (members-only).
	raw, err := nntpclient.New("tcp", addr)
	if err != nil {
		t.Fatalf("raw dial: %v", err)
	}
	if _, err := raw.List("active"); err == nil {
		t.Fatal("unauthenticated LIST must be refused")
	}
	_ = raw.Close()

	// A member connects, posts, and reads it back.
	r, err := Dial(addr, "alice")
	if err != nil {
		t.Fatalf("member dial: %v", err)
	}
	defer r.Close()

	if err := r.Post("pfs.general", "First post", "", "Hello, Usenet.\n"); err != nil {
		t.Fatalf("post: %v", err)
	}

	groups, err := r.Groups()
	if err != nil {
		t.Fatalf("groups: %v", err)
	}
	var general bool
	for _, g := range groups {
		if g.Name == "pfs.general" {
			general = true
		}
	}
	if !general {
		t.Fatalf("pfs.general not listed: %+v", groups)
	}

	g, err := r.Select("pfs.general")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if g.High < 1 {
		t.Fatalf("expected at least one article, high=%d", g.High)
	}

	ov, err := r.Overview(g.Low, g.High)
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if len(ov) != 1 || ov[0].Subject != "First post" {
		t.Fatalf("overview wrong: %+v", ov)
	}
	// From is stamped to the member, not anything client-supplied.
	if !strings.Contains(ov[0].From, "alice") {
		t.Fatalf("From not stamped: %q", ov[0].From)
	}

	art, err := r.Article(ov[0].Num)
	if err != nil {
		t.Fatalf("article: %v", err)
	}
	if !strings.Contains(art, "Hello, Usenet.") || !strings.Contains(art, "Subject: First post") {
		t.Fatalf("article body/headers wrong: %q", art)
	}
}
