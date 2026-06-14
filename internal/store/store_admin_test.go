package store

import (
	"path/filepath"
	"testing"
)

func openTest(t *testing.T) Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestBanAndListUsers(t *testing.T) {
	st := openTest(t)

	a, _ := st.EnsureUser("alice", "member", "SHA256:aaa")
	if a.Banned {
		t.Fatal("new user must not be banned")
	}
	_, _ = st.EnsureUser("bob", "member", "SHA256:bbb")

	users, err := st.ListUsers(10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("want 2 users, got %d", len(users))
	}
	// Newest-first ordering.
	if users[0].Name != "bob" {
		t.Fatalf("want bob first, got %s", users[0].Name)
	}

	if err := st.SetBanned(a.ID, true); err != nil {
		t.Fatalf("ban: %v", err)
	}
	got, _, _ := st.UserByFingerprint("SHA256:aaa")
	if !got.Banned {
		t.Fatal("alice should be banned")
	}
	if err := st.SetBanned(a.ID, false); err != nil {
		t.Fatalf("unban: %v", err)
	}
	got, _, _ = st.UserByFingerprint("SHA256:aaa")
	if got.Banned {
		t.Fatal("alice should be unbanned")
	}
}

func TestAdminAuditLog(t *testing.T) {
	st := openTest(t)

	if acts, _ := st.RecentAdminActions(10); len(acts) != 0 {
		t.Fatalf("expected empty log, got %d", len(acts))
	}
	if err := st.LogAdminAction("anthony", "ban", "spammer", "abuse"); err != nil {
		t.Fatalf("log: %v", err)
	}
	_ = st.LogAdminAction("anthony", "disable-plugin", "arcade", "")

	acts, err := st.RecentAdminActions(10)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(acts) != 2 {
		t.Fatalf("want 2 actions, got %d", len(acts))
	}
	// Newest-first.
	if acts[0].Action != "disable-plugin" || acts[0].Target != "arcade" {
		t.Fatalf("unexpected first action: %+v", acts[0])
	}
	if acts[1].Admin != "anthony" || acts[1].Detail != "abuse" {
		t.Fatalf("unexpected second action: %+v", acts[1])
	}
}

func TestRecentSessions(t *testing.T) {
	st := openTest(t)

	u, _ := st.EnsureUser("carol", "member", "SHA256:ccc")
	id, err := st.RecordSession(u.ID, "carol", "1.2.3.4", "hub")
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	_, _ = st.RecordSession(0, "bbs", "5.6.7.8", "hub")

	rows, err := st.RecentSessions(10)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(rows))
	}
	// The first recorded session is still open until ended.
	for _, r := range rows {
		if r.ID == id && r.EndedValid {
			t.Fatal("open session should not have an end time")
		}
	}
	if err := st.EndSession(id); err != nil {
		t.Fatalf("end: %v", err)
	}
	rows, _ = st.RecentSessions(10)
	var found bool
	for _, r := range rows {
		if r.ID == id {
			found = true
			if !r.EndedValid {
				t.Fatal("ended session should have an end time")
			}
		}
	}
	if !found {
		t.Fatal("ended session missing from recent list")
	}
}

func TestPluginState(t *testing.T) {
	st := openTest(t)

	if d, _ := st.DisabledPlugins(); len(d) != 0 {
		t.Fatalf("expected nothing disabled, got %v", d)
	}
	if err := st.SetPluginDisabled("arcade", true); err != nil {
		t.Fatalf("disable: %v", err)
	}
	d, err := st.DisabledPlugins()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !d["arcade"] {
		t.Fatal("arcade should be disabled")
	}
	// Idempotent re-enable.
	if err := st.SetPluginDisabled("arcade", false); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if d, _ := st.DisabledPlugins(); d["arcade"] {
		t.Fatal("arcade should be enabled again")
	}
}

func TestRecentChatsAll(t *testing.T) {
	st := openTest(t)

	u, _ := st.EnsureUser("dave", "member", "SHA256:ddd")
	_ = st.AddChat(u.ID, "dave", "user", "hello there")
	_ = st.AddChat(u.ID, "dave", "agent", "hi dave")

	chats, err := st.RecentChatsAll(10)
	if err != nil {
		t.Fatalf("recent chats: %v", err)
	}
	if len(chats) != 2 {
		t.Fatalf("want 2 chats, got %d", len(chats))
	}
	// Newest-first.
	if chats[0].Role != "agent" || chats[0].Username != "dave" {
		t.Fatalf("unexpected first chat: %+v", chats[0])
	}
}
