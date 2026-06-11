package store

import (
	"path/filepath"
	"testing"
)

func TestEmailVerificationRoundtrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	u, err := st.EnsureUser("alice", "member", "SHA256:aaa")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if u.EmailVerified {
		t.Fatal("new user should be unverified")
	}

	if err := st.SetEmailVerification(u.ID, "alice@example.com", "tok123"); err != nil {
		t.Fatalf("set: %v", err)
	}
	// Email is stored but not yet verified.
	got, _, err := st.UserByFingerprint("SHA256:aaa")
	if err != nil {
		t.Fatalf("byfp: %v", err)
	}
	if got.Email != "alice@example.com" || got.EmailVerified {
		t.Fatalf("pre-verify state wrong: %+v", got)
	}

	// Wrong token does nothing.
	if _, ok, _ := st.VerifyEmail("nope"); ok {
		t.Fatal("bad token should not verify")
	}

	// Correct token verifies and returns the account.
	vu, ok, err := st.VerifyEmail("tok123")
	if err != nil || !ok {
		t.Fatalf("verify: ok=%v err=%v", ok, err)
	}
	if vu.Name != "alice" || !vu.EmailVerified {
		t.Fatalf("verify returned wrong user: %+v", vu)
	}

	// Token is single-use (cleared after verification).
	if _, ok, _ := st.VerifyEmail("tok123"); ok {
		t.Fatal("token should be consumed after first use")
	}
	final, _, _ := st.UserByName("alice")
	if !final.EmailVerified {
		t.Fatal("user should remain verified")
	}
}

func TestVerifyEmailEmptyToken(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if _, ok, err := st.VerifyEmail(""); ok || err != nil {
		t.Fatalf("empty token must be a clean miss: ok=%v err=%v", ok, err)
	}
}
