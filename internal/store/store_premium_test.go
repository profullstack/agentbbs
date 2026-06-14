package store

import (
	"path/filepath"
	"testing"
)

func TestConfirmEmailCode(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	u, err := st.EnsureUser("bob", "member", "SHA256:bbb")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := st.SetEmailVerification(u.ID, "bob@example.com", "123456"); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Empty and wrong codes are clean misses.
	if _, ok, err := st.ConfirmEmailCode(u.ID, ""); ok || err != nil {
		t.Fatalf("empty code: ok=%v err=%v", ok, err)
	}
	if _, ok, _ := st.ConfirmEmailCode(u.ID, "000000"); ok {
		t.Fatal("wrong code should not confirm")
	}
	// The right code belonging to another user must not confirm (codes are
	// scoped per-user since they are short and collide).
	other, _ := st.EnsureUser("carol", "member", "SHA256:ccc")
	if _, ok, _ := st.ConfirmEmailCode(other.ID, "123456"); ok {
		t.Fatal("code must be scoped to its own user")
	}

	// Correct code for the right user verifies, and is single-use.
	vu, ok, err := st.ConfirmEmailCode(u.ID, "123456")
	if err != nil || !ok || !vu.EmailVerified {
		t.Fatalf("confirm: ok=%v err=%v verified=%v", ok, err, vu.EmailVerified)
	}
	if _, ok, _ := st.ConfirmEmailCode(u.ID, "123456"); ok {
		t.Fatal("code should be consumed after first use")
	}
}

func TestGrantPremium(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	u, _ := st.EnsureUser("dave", "member", "SHA256:ddd")
	if u.Premium {
		t.Fatal("new user must not be premium")
	}
	if err := st.GrantPremium(u.ID, "abbs-premium-deadbeef"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	got, _, _ := st.UserByFingerprint("SHA256:ddd")
	if !got.Premium {
		t.Fatalf("user should be premium after grant: %+v", got)
	}
	// Idempotent.
	if err := st.GrantPremium(u.ID, "abbs-premium-deadbeef"); err != nil {
		t.Fatalf("re-grant: %v", err)
	}
}
