package mailbox

import (
	"context"
	"errors"
	"testing"
	"time"
)

func seeded() *MemoryTransport {
	m := NewMemoryTransport()
	m.Add(Message{MessageSummary: MessageSummary{UID: 1, Mailbox: Inbox, From: Address{Name: "Carol", Address: "carol@example.com"}, Subject: "Welcome", Date: time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)}, Text: "hi alice"})
	m.Add(Message{MessageSummary: MessageSummary{UID: 2, Mailbox: Inbox, From: Address{Address: "deploy@ci.example.com"}, Subject: "Build passed", Date: time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC)}, Text: "all green"})
	return m
}

func paidClient(t Transport) *Client {
	return NewClient(t, Identity{Name: "alice", Paid: true}, "mail.profullstack.com", 50)
}

func TestParseFormatAddress(t *testing.T) {
	a := ParseAddress("Ada Lovelace <ada@x.com>")
	if a.Name != "Ada Lovelace" || a.Address != "ada@x.com" {
		t.Fatalf("parse named: %+v", a)
	}
	if got := ParseAddress("ada@x.com"); got.Address != "ada@x.com" || got.Name != "" {
		t.Fatalf("parse bare: %+v", got)
	}
	if got := FormatAddress(Address{Name: "Doe, John", Address: "j@x.com"}); got != `"Doe, John" <j@x.com>` {
		t.Fatalf("format specials: %q", got)
	}
}

func TestValidEmailAndDraft(t *testing.T) {
	if !ValidEmail("a@b.com") || ValidEmail("nope") {
		t.Fatal("ValidEmail")
	}
	if _, err := NormalizeDraft(Draft{Subject: "x", Text: "y"}); err == nil {
		t.Fatal("expected error for no recipients")
	}
	d, err := NormalizeDraft(Draft{To: []Address{{Address: " a@b.com "}}, Subject: "  hi  ", Text: "z"})
	if err != nil || len(d.To) != 1 || d.To[0].Address != "a@b.com" || d.Subject != "hi" {
		t.Fatalf("normalize: %+v err=%v", d, err)
	}
}

func TestGate(t *testing.T) {
	c := NewClient(seeded(), Identity{Name: "bob", Paid: false}, "mail.profullstack.com", 0)
	if _, err := c.Inbox(context.Background(), 0); !errors.Is(err, ErrNotPaid) {
		t.Fatalf("expected ErrNotPaid, got %v", err)
	}
}

// Inbox is a thin helper used in tests and bot mode.
func (c *Client) Inbox(ctx context.Context, limit int) ([]MessageSummary, error) {
	return c.List(ctx, Inbox, limit)
}

func TestListNewestFirst(t *testing.T) {
	c := paidClient(seeded())
	got, err := c.List(context.Background(), Inbox, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].UID != 2 {
		t.Fatalf("expected newest first [2,1], got %+v", got)
	}
}

func TestReadMarksSeenAndPeek(t *testing.T) {
	tr := seeded()
	c := paidClient(tr)
	if _, ok, _ := c.Read(context.Background(), Inbox, 1, false); !ok {
		t.Fatal("read 1")
	}
	if m, _, _ := tr.ReadMessage(context.Background(), Inbox, 1); !m.Seen {
		t.Fatal("uid 1 should be seen")
	}
	if _, _, _ = c.Read(context.Background(), Inbox, 2, true); true {
		if m, _, _ := tr.ReadMessage(context.Background(), Inbox, 2); m.Seen {
			t.Fatal("peek must not mark seen")
		}
	}
	if _, ok, _ := c.Read(context.Background(), Inbox, 999, false); ok {
		t.Fatal("unknown uid should be ok=false")
	}
}

func TestSearch(t *testing.T) {
	c := paidClient(seeded())
	hits, _ := c.Search(context.Background(), "green", "", 0)
	if len(hits) != 1 || hits[0].UID != 2 {
		t.Fatalf("search green: %+v", hits)
	}
	hits, _ = c.Search(context.Background(), "carol@example.com", "", 0)
	if len(hits) != 1 || hits[0].UID != 1 {
		t.Fatalf("search sender: %+v", hits)
	}
}

func TestSendAndReply(t *testing.T) {
	tr := seeded()
	c := paidClient(tr)
	res, err := c.Send(context.Background(), Draft{To: []Address{{Address: "carol@example.com"}}, Subject: "  Hi  ", Text: "yo"})
	if err != nil || res.MessageID == "" {
		t.Fatalf("send: %v", err)
	}
	sent, _ := tr.ListMessages(context.Background(), ListOptions{Mailbox: Sent})
	if len(sent) != 1 || sent[0].From.Address != "alice@mail.profullstack.com" || sent[0].Subject != "Hi" {
		t.Fatalf("sent: %+v", sent)
	}

	orig, _, _ := c.Read(context.Background(), Inbox, 1, true)
	if _, err := c.Reply(context.Background(), orig, "thanks", false); err != nil {
		t.Fatal(err)
	}
	sent, _ = tr.ListMessages(context.Background(), ListOptions{Mailbox: Sent})
	var re MessageSummary
	for _, s := range sent {
		if s.Subject == "Re: Welcome" {
			re = s
		}
	}
	if re.Subject != "Re: Welcome" || re.To[0].Address != "carol@example.com" {
		t.Fatalf("reply: %+v", sent)
	}
}

func TestFlagAndDelete(t *testing.T) {
	tr := seeded()
	c := paidClient(tr)
	if err := c.Flag(context.Background(), Inbox, 1, true); err != nil {
		t.Fatal(err)
	}
	if m, _, _ := tr.ReadMessage(context.Background(), Inbox, 1); !m.Flagged {
		t.Fatal("uid 1 should be flagged")
	}
	if err := c.Delete(context.Background(), Inbox, 1); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := tr.ReadMessage(context.Background(), Inbox, 1); ok {
		t.Fatal("uid 1 should be deleted")
	}
}
