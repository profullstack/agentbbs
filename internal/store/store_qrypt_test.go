package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestQryptInviteQuota(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	const quota = 3

	if n, err := st.QryptInviteCount("alice"); err != nil || n != 0 {
		t.Fatalf("initial count = %d, %v; want 0, nil", n, err)
	}

	for i := 0; i < quota; i++ {
		jti := "jti-alice-" + string(rune('a'+i))
		if err := st.RecordQryptInvite("alice", jti, quota); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	if n, err := st.QryptInviteCount("alice"); err != nil || n != quota {
		t.Fatalf("count after fill = %d, %v; want %d", n, err, quota)
	}

	// One over the cap must be rejected with ErrQuotaExceeded and not stored.
	if err := st.RecordQryptInvite("alice", "jti-over", quota); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("over-quota err = %v; want ErrQuotaExceeded", err)
	}
	if n, _ := st.QryptInviteCount("alice"); n != quota {
		t.Fatalf("count after rejected insert = %d; want %d", n, quota)
	}

	// Quotas are per-member: bob is unaffected.
	if err := st.RecordQryptInvite("bob", "jti-bob", quota); err != nil {
		t.Fatalf("bob record: %v", err)
	}
	if n, _ := st.QryptInviteCount("bob"); n != 1 {
		t.Fatalf("bob count = %d; want 1", n)
	}

	// quota <= 0 means unlimited.
	if err := st.RecordQryptInvite("carol", "jti-carol-1", 0); err != nil {
		t.Fatalf("unlimited record: %v", err)
	}
}
