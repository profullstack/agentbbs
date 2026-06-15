package store

import "testing"

func TestMessagingRoundtrip(t *testing.T) {
	st := openTest(t)

	_, _ = st.EnsureUser("alice", "member", "SHA256:aaa")
	_, _ = st.EnsureUser("bob", "member", "SHA256:bbb")

	if n, err := st.UnreadCount("bob"); err != nil || n != 0 {
		t.Fatalf("fresh unread: n=%d err=%v", n, err)
	}

	if err := st.SendMessage("alice", "bob", "hey, c4 tonight?"); err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := st.SendMessage("alice", "bob", "second note"); err != nil {
		t.Fatalf("send2: %v", err)
	}

	n, err := st.UnreadCount("bob")
	if err != nil || n != 2 {
		t.Fatalf("unread after send: n=%d err=%v", n, err)
	}

	inbox, err := st.Inbox("bob", 10)
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	if len(inbox) != 2 {
		t.Fatalf("want 2 messages, got %d", len(inbox))
	}
	// Newest first.
	if inbox[0].Body != "second note" || inbox[0].From != "alice" || inbox[0].To != "bob" {
		t.Fatalf("unexpected newest message: %+v", inbox[0])
	}

	// Mark only the first read; the other stays unread.
	if err := st.MarkRead("bob", []int64{inbox[0].ID}); err != nil {
		t.Fatalf("markread: %v", err)
	}
	if n, _ := st.UnreadCount("bob"); n != 1 {
		t.Fatalf("want 1 unread after partial read, got %d", n)
	}

	// MarkRead is scoped to the recipient: alice can't clear bob's mail.
	if err := st.MarkRead("alice", []int64{inbox[1].ID}); err != nil {
		t.Fatalf("markread other: %v", err)
	}
	if n, _ := st.UnreadCount("bob"); n != 1 {
		t.Fatalf("cross-user markread leaked: unread=%d", n)
	}

	// Empty ids is a no-op.
	if err := st.MarkRead("bob", nil); err != nil {
		t.Fatalf("markread empty: %v", err)
	}
}

func TestOnlineUsers(t *testing.T) {
	st := openTest(t)

	u, _ := st.EnsureUser("carol", "member", "SHA256:ccc")
	id, _ := st.RecordSession(u.ID, "carol", "1.2.3.4", "hub")

	online, err := st.OnlineUsers()
	if err != nil {
		t.Fatalf("online: %v", err)
	}
	if !online["carol"] {
		t.Fatal("carol should be online while her session is open")
	}

	if err := st.EndSession(id); err != nil {
		t.Fatalf("end: %v", err)
	}
	online, _ = st.OnlineUsers()
	if online["carol"] {
		t.Fatal("carol should be offline after her session ends")
	}
}
