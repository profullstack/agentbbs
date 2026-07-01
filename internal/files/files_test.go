package files

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkg/sftp"
	"github.com/profullstack/agentbbs/internal/store"
)

func TestEnsureUserPubSeedsReadme(t *testing.T) {
	svc, _, u := newTestService(t)
	if err := svc.ensureUserPub(u.Name); err != nil {
		t.Fatal(err)
	}
	readme := filepath.Join(svc.userPub(u.Name), "README.txt")
	got, err := os.ReadFile(readme)
	if err != nil {
		t.Fatalf("public area not seeded with README.txt: %v", err)
	}
	if !bytes.Equal(got, defaultPublicReadme) {
		t.Errorf("seeded README content does not match the embedded default")
	}
	// Re-materialization must not clobber a member's own README.
	custom := []byte("this is my own readme, hands off\n")
	if err := os.WriteFile(readme, custom, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := svc.ensureUserPub(u.Name); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(readme); !bytes.Equal(got, custom) {
		t.Errorf("ensureUserPub overwrote the member's own README: %q", got)
	}
}

func newTestService(t *testing.T) (*Service, store.Store, store.User) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc, err := New(st, Config{Root: filepath.Join(dir, "files"), DefaultQuota: 1 << 20})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	u, err := st.EnsureUser("alice", "member", "SHA256:alicekey")
	if err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	return svc, st, u
}

func TestResolveConfinement(t *testing.T) {
	svc, _, u := newTestService(t)
	sess, err := svc.newSession(u)
	if err != nil {
		t.Fatal(err)
	}
	priv := svc.privRoot(u.Name)
	pub := svc.pubRoot()

	// These must never resolve to a path outside their area root. Some are
	// expected to error outright; for the rest, assert containment.
	escapes := []string{
		"/me/../../../etc/passwd",
		"/me/../../etc",
		"/me/sub/../../../../etc/shadow",
		"/public/../me/secret",
		"/public/../../etc",
		"/../etc/passwd",
		"/me/./../../public/../../root",
	}
	for _, p := range escapes {
		res, err := sess.resolve(p)
		if err != nil {
			continue // rejected outright — fine
		}
		if res.root {
			continue // collapsed to the synthetic root — fine
		}
		ok := within(priv, res.real) || within(pub, res.real)
		if !ok {
			t.Errorf("resolve(%q) escaped: %q (priv=%q pub=%q)", p, res.real, priv, pub)
		}
	}
}

func TestResolveAreas(t *testing.T) {
	svc, _, u := newTestService(t)
	sess, _ := svc.newSession(u)

	root, _ := sess.resolve("/")
	if !root.root {
		t.Error("/ should be the synthetic root")
	}
	me, err := sess.resolve("/me/notes.txt")
	if err != nil || me.area != areaMe || !me.writable {
		t.Errorf("/me should be writable me-area: %+v err=%v", me, err)
	}
	if !within(svc.privRoot(u.Name), me.real) {
		t.Errorf("/me path %q not under priv root", me.real)
	}
	pub, err := sess.resolve("/public/shared.txt")
	if err != nil || pub.area != areaPublic {
		t.Errorf("/public should be public area: %+v err=%v", pub, err)
	}
	if _, err := sess.resolve("/etc/passwd"); err == nil {
		t.Error("unknown top-level area should be rejected")
	}
}

func TestSymlinkEscapeBlocked(t *testing.T) {
	svc, _, u := newTestService(t)
	sess, _ := svc.newSession(u)
	priv := svc.privRoot(u.Name)

	// Plant a symlink inside the workspace pointing at the system root.
	link := filepath.Join(priv, "escape")
	if err := os.Symlink("/etc", link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := sess.resolve("/me/escape/passwd"); err == nil {
		t.Error("path through an escaping symlink must be rejected")
	}
}

func TestOwnPublicWritable(t *testing.T) {
	svc, _, u := newTestService(t)

	// A member's /public is their own area (served at ~name/public) — they can
	// always write to it, and it resolves under <root>/public/<name>.
	sess, _ := svc.newSession(u)
	if err := sess.Filecmd(sftp.NewRequest("Mkdir", "/public/uploads")); err != nil {
		t.Fatalf("own /public mkdir should succeed: %v", err)
	}
	res, err := sess.resolve("/public/uploads")
	if err != nil || !res.writable {
		t.Fatalf("own /public should resolve writable: %+v err=%v", res, err)
	}
	if want := svc.userPub(u.Name); !within(want, res.real) && res.real != want {
		t.Errorf("/public resolved to %q, want under %q", res.real, want)
	}
}

func TestQuotaEnforced(t *testing.T) {
	svc, _, u := newTestService(t)
	sess, _ := svc.newSession(u)
	sess.quota = 100   // tiny
	sess.used.Store(0) // isolate the writer from the seeded-README baseline

	f, err := os.Create(filepath.Join(svc.privRoot(u.Name), "big"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := &quotaWriter{f: f, sess: sess}

	if _, err := w.WriteAt(make([]byte, 80), 0); err != nil {
		t.Fatalf("write within quota failed: %v", err)
	}
	if _, err := w.WriteAt(make([]byte, 80), 80); err != sftp.ErrSSHFxFailure {
		t.Errorf("write over quota should fail, got %v", err)
	}
}

func TestWebSaveOverQuotaPreservesExistingFile(t *testing.T) {
	svc, _, u := newTestService(t)
	sess, _ := svc.newSession(u)
	sess.quota = 5

	if _, err := sess.webSave("/me/note.txt", strings.NewReader("ok")); err != nil {
		t.Fatalf("initial save failed: %v", err)
	}
	if _, err := sess.webSave("/me/note.txt", strings.NewReader("too-large")); err != errQuota {
		t.Fatalf("over-quota replace: got %v, want errQuota", err)
	}

	got, err := os.ReadFile(filepath.Join(svc.privRoot(u.Name), "note.txt"))
	if err != nil {
		t.Fatalf("existing file should remain after failed replace: %v", err)
	}
	if string(got) != "ok" {
		t.Fatalf("existing file changed after failed replace: %q", got)
	}
}

func TestUsage(t *testing.T) {
	svc, _, u := newTestService(t)
	if err := svc.ensureWorkspace(u.Name); err != nil {
		t.Fatal(err)
	}
	want := []byte(strings.Repeat("x", 512))
	if err := os.WriteFile(filepath.Join(svc.privRoot(u.Name), "a.txt"), want, 0o644); err != nil {
		t.Fatal(err)
	}
	usage, err := svc.Usage(u)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Bytes != 512 {
		t.Errorf("usage = %d, want 512", usage.Bytes)
	}
	if usage.Quota != 1<<20 {
		t.Errorf("quota = %d, want default", usage.Quota)
	}
}

func TestUsageCountsOwnedAreas(t *testing.T) {
	svc, _, u := newTestService(t)
	if err := svc.ensureWorkspace(u.Name); err != nil {
		t.Fatal(err)
	}
	if err := svc.ensureUserPub(u.Name); err != nil {
		t.Fatal(err)
	}
	// ensureUserPub seeds a default README.txt into the public area; it counts
	// toward the gauge like any other public file.
	seed := int64(len(defaultPublicReadme))
	// Both of the member's owned areas — private /me and their public /public
	// (<root>/public/<name>) — count toward the quota gauge. Another member's
	// public area does not.
	if err := os.WriteFile(filepath.Join(svc.privRoot(u.Name), "a.txt"), []byte(strings.Repeat("x", 512)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svc.userPub(u.Name), "b.txt"), []byte(strings.Repeat("y", 256)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(svc.userPub("someoneelse"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svc.userPub("someoneelse"), "x.txt"), []byte(strings.Repeat("z", 9999)), 0o644); err != nil {
		t.Fatal(err)
	}
	usage, err := svc.Usage(u)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(768) + seed; usage.Bytes != want {
		t.Errorf("usage = %d, want %d (512 /me + 256 /public + seeded README, other members excluded)", usage.Bytes, want)
	}
}

func TestSafeJoinSymlinkedRoot(t *testing.T) {
	// A storage root reached through a symlink must not cause valid child paths
	// to be rejected. Before the fix, EvalSymlinks resolved the probe to the
	// canonical path while within() still compared against the lexical root,
	// producing a false-positive errEscape.
	dir := t.TempDir()
	realRoot := filepath.Join(dir, "real")
	if err := os.MkdirAll(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	linkRoot := filepath.Join(dir, "link")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	// A normal file inside the real root, accessed via the symlinked root.
	target := filepath.Join(realRoot, "notes.txt")
	if err := os.WriteFile(target, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := safeJoin(linkRoot, "notes.txt")
	if err != nil {
		t.Fatalf("safeJoin with symlinked root rejected valid path: %v", err)
	}
	if want := filepath.Join(linkRoot, "notes.txt"); got != want {
		t.Errorf("safeJoin = %q, want %q", got, want)
	}
}

func TestSafeJoinChildSymlinkEscapeStillBlocked(t *testing.T) {
	// A child symlink inside the area that points outside must still be
	// rejected, even when the root itself is a symlink.
	dir := t.TempDir()
	realRoot := filepath.Join(dir, "real")
	if err := os.MkdirAll(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	linkRoot := filepath.Join(dir, "link")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	// Plant an escaping symlink inside the (real) workspace.
	escape := filepath.Join(realRoot, "escape")
	if err := os.Symlink("/etc", escape); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := safeJoin(linkRoot, "escape/passwd"); err == nil {
		t.Error("path through an escaping child symlink must be rejected")
	}
}

func TestRevokeBlocksAndQuotaOverride(t *testing.T) {
	svc, st, u := newTestService(t)
	if err := st.SetFilesQuota(u.ID, 4096); err != nil {
		t.Fatal(err)
	}
	if got := svc.quotaFor(u.ID); got != 4096 {
		t.Errorf("quotaFor = %d, want 4096 override", got)
	}
	if err := st.SetFilesRevoked(u.ID, true); err != nil {
		t.Fatal(err)
	}
	fa, err := st.FilesAccess(u.ID)
	if err != nil || !fa.Revoked {
		t.Errorf("FilesAccess revoked = %+v err=%v", fa, err)
	}
}
