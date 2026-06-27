package ircpass

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfiguredRequiresScript(t *testing.T) {
	if (Config{}).Configured() {
		t.Fatal("empty config should not be Configured")
	}
	if !(Config{Script: "/x/set-irc-password.sh"}).Configured() {
		t.Fatal("config with a script path should be Configured")
	}
}

func TestSetPasswordRunsHelperWithArgs(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "args.txt")
	script := filepath.Join(dir, "set-irc-password.sh")
	// A fake helper mirroring the real contract: member in $1, "-" in $2, and the
	// password on stdin. Records member + stdin so the test can assert both.
	body := "#!/bin/sh\nread pw\nprintf '%s\\n%s\\n%s\\n' \"$1\" \"$2\" \"$pw\" > " + out + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	c := Config{Script: script, UseSudo: false}
	if err := c.SetPassword("alice", "s3cret-pw"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	// member as argv[0], "-" sentinel as argv[1], password only on stdin.
	want := "alice\n-\ns3cret-pw\n"
	if string(got) != want {
		t.Fatalf("helper got %q, want %q", got, want)
	}
}

func TestSetPasswordRejectsBadMember(t *testing.T) {
	c := Config{Script: "/bin/true", UseSudo: false}
	for _, bad := range []string{"", "-rf", "a b", "alice;rm", "../etc", "Alice"} {
		if err := c.SetPassword(bad, "pw"); err == nil {
			t.Fatalf("expected error for member %q", bad)
		}
	}
}

func TestSetPasswordRejectsBadPassword(t *testing.T) {
	for _, bad := range []string{"", "with\nnewline", "carriage\rreturn"} {
		if err := (Config{Script: "/bin/true"}).SetPassword("alice", bad); err == nil {
			t.Fatalf("expected error for password %q", bad)
		}
	}
}

func TestSetPasswordUnconfigured(t *testing.T) {
	if err := (Config{}).SetPassword("alice", "pw"); err == nil ||
		!strings.Contains(err.Error(), "not configured") {
		t.Fatalf("want not-configured error, got %v", err)
	}
}

func TestSetPasswordSurfacesHelperFailure(t *testing.T) {
	c := Config{Script: "/bin/false", UseSudo: false}
	if err := c.SetPassword("alice", "pw"); err == nil {
		t.Fatal("expected error when helper exits non-zero")
	}
}
