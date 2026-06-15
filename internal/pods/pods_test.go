package pods

import (
	"os"
	"path/filepath"
	"testing"
)

// When no users dir is configured the bind is disabled: both return values are
// empty and no directory is created.
func TestPublicHTMLMountDisabled(t *testing.T) {
	m := &Manager{} // usersDir == ""
	host, spec := m.publicHTMLMount("chovy")
	if host != "" || spec != "" {
		t.Fatalf("expected empty host/spec when usersDir unset, got host=%q spec=%q", host, spec)
	}
}

// With a users dir set, the member's public_html is created on the host and the
// volume spec maps it to /home/dev/public_html in the pod.
func TestPublicHTMLMountCreatesAndMaps(t *testing.T) {
	users := t.TempDir()
	m := &Manager{usersDir: users}

	host, spec := m.publicHTMLMount("chovy")

	wantHost := filepath.Join(users, "chovy", "public_html")
	if host != wantHost {
		t.Fatalf("host = %q, want %q", host, wantHost)
	}
	if want := wantHost + ":/home/dev/public_html"; spec != want {
		t.Fatalf("spec = %q, want %q", spec, want)
	}
	if fi, err := os.Stat(wantHost); err != nil || !fi.IsDir() {
		t.Fatalf("expected %q to be a created directory, err=%v", wantHost, err)
	}
}
