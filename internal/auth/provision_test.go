package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	gossh "golang.org/x/crypto/ssh"
)

func TestFingerprintAuthorizedKey(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	authLine := string(gossh.MarshalAuthorizedKey(sshPub)) // "ssh-ed25519 AAAA…\n"
	want := gossh.FingerprintSHA256(sshPub)

	// Bare line.
	got, err := FingerprintAuthorizedKey(authLine)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Fatalf("fingerprint = %q, want %q", got, want)
	}

	// With a trailing comment + surrounding whitespace.
	got2, err := FingerprintAuthorizedKey("  " + strings.TrimRight(authLine, "\n") + " acme@dev  ")
	if err != nil {
		t.Fatalf("unexpected error with comment: %v", err)
	}
	if got2 != want {
		t.Fatalf("fingerprint with comment = %q, want %q", got2, want)
	}
}

func TestFingerprintAuthorizedKeyRejectsGarbage(t *testing.T) {
	if _, err := FingerprintAuthorizedKey("not a key"); err == nil {
		t.Fatal("expected error for non-key input")
	}
	if _, err := FingerprintAuthorizedKey(""); err == nil {
		t.Fatal("expected error for empty input")
	}
}
