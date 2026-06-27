package main

import (
	"path/filepath"
	"sort"
	"testing"

	"github.com/profullstack/agentbbs/internal/store"
)

func testStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestResolveRecipients(t *testing.T) {
	st := testStore(t)
	for _, n := range []string{"alice", "bob", "carol", "dave"} {
		if _, err := st.EnsureUser(n, "member", "SHA256:"+n); err != nil {
			t.Fatal(err)
		}
	}
	// Ban dave so "all" skips him.
	u, _, _ := st.UserByName("dave")
	if err := st.SetBanned(u.ID, true); err != nil {
		t.Fatal(err)
	}
	a := &app{st: st}

	t.Run("comma list dedupes, lowercases, excludes sender", func(t *testing.T) {
		got, unknown, err := a.resolveRecipients("Bob,carol,bob,alice", "alice")
		if err != nil || len(unknown) != 0 {
			t.Fatalf("err=%v unknown=%v", err, unknown)
		}
		sort.Strings(got)
		if len(got) != 2 || got[0] != "bob" || got[1] != "carol" {
			t.Fatalf("recipients = %v", got)
		}
	})

	t.Run("unknown names are reported", func(t *testing.T) {
		got, unknown, err := a.resolveRecipients("bob,nobody", "alice")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0] != "bob" {
			t.Fatalf("recipients = %v", got)
		}
		if len(unknown) != 1 || unknown[0] != "nobody" {
			t.Fatalf("unknown = %v", unknown)
		}
	})

	t.Run("all expands to every non-banned member except sender", func(t *testing.T) {
		for _, tok := range []string{"all", "ALL", "*", "everyone", "@all"} {
			got, _, err := a.resolveRecipients(tok, "alice")
			if err != nil {
				t.Fatalf("%s: %v", tok, err)
			}
			sort.Strings(got)
			// alice excluded (sender), dave excluded (banned) → bob, carol.
			if len(got) != 2 || got[0] != "bob" || got[1] != "carol" {
				t.Fatalf("%s → %v", tok, got)
			}
		}
	})
}
