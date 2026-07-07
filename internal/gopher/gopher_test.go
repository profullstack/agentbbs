package gopher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/profullstack/agentbbs/internal/store"
)

// fakeContent is an in-memory Content for the resolver tests (no real store).
type fakeContent struct {
	users    []store.User
	groups   []store.NewsGroup
	articles map[string][]store.NewsArticle
}

func (f *fakeContent) ListUsers(int) ([]store.User, error)    { return f.users, nil }
func (f *fakeContent) NewsGroups() ([]store.NewsGroup, error) { return f.groups, nil }
func (f *fakeContent) NewsArticlesRange(g string, from, to int64) ([]store.NewsArticle, error) {
	var out []store.NewsArticle
	for _, a := range f.articles[g] {
		if a.Num >= from && a.Num <= to {
			out = append(out, a)
		}
	}
	return out, nil
}
func (f *fakeContent) NewsArticleByNum(g string, num int64) (store.NewsArticle, bool, error) {
	for _, a := range f.articles[g] {
		if a.Num == num {
			return a, true, nil
		}
	}
	return store.NewsArticle{}, false, nil
}

// newTestServer wires a Server over a temp data dir with two members (one
// banned), two newsgroups (one public), and a homepage tree for alice.
func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	dataDir := t.TempDir()
	// alice's homepage: index.html (text) + assets/logo.png (binary) + subdir.
	home := filepath.Join(dataDir, "users", "alice", "public_html")
	mustMkdir(t, filepath.Join(home, "assets"))
	mustWrite(t, filepath.Join(home, "index.html"), "<h1>hi</h1>")
	mustWrite(t, filepath.Join(home, "assets", "logo.png"), "\x89PNGbinary")
	// a file OUTSIDE any member area, to prove traversal cannot reach it.
	mustWrite(t, filepath.Join(dataDir, "secret.txt"), "TOP SECRET")

	fc := &fakeContent{
		users: []store.User{
			{Name: "alice"},
			{Name: "bob", Banned: true},
			{Name: "carol"},
		},
		groups: []store.NewsGroup{
			{Name: "pfs.announce", Description: "Announcements", High: 1},
			{Name: "pfs.secret", Description: "Members only", High: 1},
		},
		articles: map[string][]store.NewsArticle{
			"pfs.announce": {{Group: "pfs.announce", Num: 1, Subject: "Welcome", From: "sysop", Body: "hello\n.\nworld"}},
			"pfs.secret":   {{Group: "pfs.secret", Num: 1, Subject: "Hush", From: "sysop", Body: "shh"}},
		},
	}
	srv := New(fc, dataDir, "gopher.test", "70", []string{"pfs.announce"},
		func() string { return "About body here" })
	return srv, dataDir
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}
func mustWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMenuWireFormat(t *testing.T) {
	m := NewMenu("h.test", "70")
	m.Info("hi").Link(TypeMenu, "Members", "/members")
	got := m.Render()
	// Info row is type 'i'; the link row is tab-delimited with host/port; the
	// whole thing ends in the RFC 1436 dot line.
	if !strings.HasPrefix(got, "ihi\t") {
		t.Errorf("info row missing/malformed: %q", got)
	}
	if !strings.Contains(got, "1Members\t/members\th.test\t70\r\n") {
		t.Errorf("link row malformed: %q", got)
	}
	if !strings.HasSuffix(got, ".\r\n") {
		t.Errorf("menu must end with the dot terminator: %q", got)
	}
}

func TestResolveRootBanner(t *testing.T) {
	srv, _ := newTestServer(t)
	pub := srv.Resolve("", false, "")
	if pub.Kind != KindMenu {
		t.Fatalf("root should be a menu, got %v", pub.Kind)
	}
	wire := string(pub.Wire())
	for _, want := range []string{"/about", "/members", "/news", "/files", "ssh gopher@gopher.test"} {
		if !strings.Contains(wire, want) {
			t.Errorf("public root missing %q\n%s", want, wire)
		}
	}
	authed := string(srv.Resolve("/", true, "alice").Wire())
	if !strings.Contains(authed, "Signed in as alice") {
		t.Errorf("authed root should greet the member:\n%s", authed)
	}
}

func TestResolveAbout(t *testing.T) {
	srv, _ := newTestServer(t)
	r := srv.Resolve("/about", false, "")
	if r.Kind != KindText || !strings.Contains(r.Text, "About body here") {
		t.Fatalf("about wrong: %+v", r)
	}
}

func TestResolveMembersExcludesBanned(t *testing.T) {
	srv, _ := newTestServer(t)
	wire := string(srv.Resolve("/members", false, "").Wire())
	if !strings.Contains(wire, "\t/~alice\t") || !strings.Contains(wire, "\t/~carol\t") {
		t.Errorf("members should list alice and carol:\n%s", wire)
	}
	if strings.Contains(wire, "/~bob") {
		t.Errorf("banned member bob must not appear:\n%s", wire)
	}
}

func TestHomepageFileAndDir(t *testing.T) {
	srv, _ := newTestServer(t)
	// directory listing
	dir := srv.Resolve("/~alice", false, "")
	if dir.Kind != KindMenu {
		t.Fatalf("homepage root should be a dir menu, got %v", dir.Kind)
	}
	dw := string(dir.Wire())
	if !strings.Contains(dw, "/~alice/index.html") || !strings.Contains(dw, "/~alice/assets") {
		t.Errorf("dir listing missing entries:\n%s", dw)
	}
	// text file
	txt := srv.Resolve("/~alice/index.html", false, "")
	if txt.Kind != KindText || !strings.Contains(txt.Text, "<h1>hi</h1>") {
		t.Errorf("index.html should be served as text: %+v", txt)
	}
	// binary file
	bin := srv.Resolve("/~alice/assets/logo.png", false, "")
	if bin.Kind != KindBinary || string(bin.Data) != "\x89PNGbinary" {
		t.Errorf("png should be binary: %+v", bin)
	}
}

func TestPathTraversalRefused(t *testing.T) {
	srv, _ := newTestServer(t)
	for _, sel := range []string{
		"/~alice/../../secret.txt",
		"/~alice/../../../etc/passwd",
		"/files/~alice/../../secret.txt",
		"/~alice/assets/../../../secret.txt",
	} {
		r := srv.Resolve(sel, true, "alice")
		if r.Kind == KindText && strings.Contains(r.Text, "TOP SECRET") {
			t.Fatalf("traversal %q leaked the secret file", sel)
		}
		if r.Kind == KindBinary && strings.Contains(string(r.Data), "TOP SECRET") {
			t.Fatalf("traversal %q leaked the secret file (binary)", sel)
		}
	}
	// A bad member name is refused outright.
	if r := srv.Resolve("/~..", false, ""); r.Kind != KindError {
		t.Errorf("bad member name should error, got %v", r.Kind)
	}
}

func TestNewsPublicVsAuthed(t *testing.T) {
	srv, _ := newTestServer(t)

	// Public group list shows only the allowlisted group.
	pub := string(srv.Resolve("/news", false, "").Wire())
	if !strings.Contains(pub, "/news/pfs.announce") {
		t.Errorf("public news should list pfs.announce:\n%s", pub)
	}
	if strings.Contains(pub, "/news/pfs.secret") {
		t.Errorf("public news must hide pfs.secret:\n%s", pub)
	}

	// Authed group list shows every group.
	authed := string(srv.Resolve("/news", true, "alice").Wire())
	if !strings.Contains(authed, "/news/pfs.secret") {
		t.Errorf("authed news should list pfs.secret:\n%s", authed)
	}

	// Public access to a members-only group is refused...
	if r := srv.Resolve("/news/pfs.secret", false, ""); r.Kind != KindError {
		t.Errorf("public access to members-only group should error, got %v", r.Kind)
	}
	// ...but the authenticated surface can read it.
	if r := srv.Resolve("/news/pfs.secret/1", true, "alice"); r.Kind != KindText || !strings.Contains(r.Text, "shh") {
		t.Errorf("authed should read the private article: %+v", r)
	}
}

func TestArticleDotStuffing(t *testing.T) {
	srv, _ := newTestServer(t)
	r := srv.Resolve("/news/pfs.announce/1", false, "")
	if r.Kind != KindText {
		t.Fatalf("article should be text, got %v", r.Kind)
	}
	// The body has a lone "." line; on the wire it must be escaped to ".." so it
	// is not read as the terminator, and the real terminator is the final line.
	wire := string(r.Wire())
	if !strings.Contains(wire, "\r\n..\r\n") {
		t.Errorf("lone dot line should be dot-stuffed to '..':\n%q", wire)
	}
	if !strings.HasSuffix(wire, "\r\n.\r\n") {
		t.Errorf("text response must end with the dot terminator:\n%q", wire)
	}
}

func TestUnknownSelector(t *testing.T) {
	srv, _ := newTestServer(t)
	r := srv.Resolve("/does/not/exist", false, "")
	if r.Kind != KindError {
		t.Fatalf("unknown selector should error, got %v", r.Kind)
	}
	if !strings.HasPrefix(string(r.Wire()), "3") {
		t.Errorf("error response should be a type-3 line: %q", r.Wire())
	}
}
