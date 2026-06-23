package mailbox

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func seeded() *MemoryTransport {
	m := NewMemoryTransport()
	m.Add(Message{MessageSummary: MessageSummary{UID: 1, Mailbox: Inbox, From: Address{Name: "Carol", Address: "carol@example.com"}, Subject: "Welcome", Date: time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)}, Text: "hi alice"})
	m.Add(Message{MessageSummary: MessageSummary{UID: 2, Mailbox: Inbox, From: Address{Address: "deploy@ci.example.com"}, Subject: "Build passed", Date: time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC)}, Text: "all green"})
	return m
}

func paidClient(t Transport) *Client {
	return NewClient(t, Identity{Name: "alice", Paid: true}, "bbs.profullstack.com", 50)
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
	// A free member (Paid: false) now has full mail access.
	c := NewClient(seeded(), Identity{Name: "bob", Paid: false}, "bbs.profullstack.com", 0)
	if _, err := c.Inbox(context.Background(), 0); err != nil {
		t.Fatalf("free member should have mail access, got %v", err)
	}
	// Only a caller without a registered handle is rejected.
	anon := NewClient(seeded(), Identity{Name: "", Paid: true}, "bbs.profullstack.com", 0)
	if _, err := anon.Inbox(context.Background(), 0); !errors.Is(err, ErrNotMember) {
		t.Fatalf("expected ErrNotMember, got %v", err)
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
	if len(sent) != 1 || sent[0].From.Address != "alice@bbs.profullstack.com" || sent[0].Subject != "Hi" {
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

func TestComposeTUISend(t *testing.T) {
	tr := seeded()
	c := paidClient(tr)
	var m tea.Model = readerModel{c: c, ctx: context.Background(), mailbox: Inbox}

	key := func(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }
	typeStr := func(s string) {
		for _, r := range s {
			m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		}
	}

	typeStr("c") // open compose from the list
	if m.(readerModel).mode != modeCompose {
		t.Fatalf("expected compose mode, got %v", m.(readerModel).mode)
	}
	typeStr("bob@example.com")
	m, _ = m.Update(key(tea.KeyEnter)) // To -> Cc
	m, _ = m.Update(key(tea.KeyEnter)) // Cc -> Subject
	typeStr("Hello")
	m, _ = m.Update(key(tea.KeyTab)) // Subject -> Body
	typeStr("first line")
	m, _ = m.Update(key(tea.KeyEnter)) // newline in body
	typeStr("second line")

	_, cmd := m.Update(key(tea.KeyCtrlD)) // send
	if cmd == nil {
		t.Fatal("ctrl+d produced no command")
	}
	if _, ok := cmd().(sentMsg); !ok {
		t.Fatalf("expected sentMsg, got %T", cmd())
	}
	sent, _ := tr.ListMessages(context.Background(), ListOptions{Mailbox: Sent})
	if len(sent) != 1 {
		t.Fatalf("want 1 sent, got %d", len(sent))
	}
	if sent[0].To[0].Address != "bob@example.com" || sent[0].Subject != "Hello" {
		t.Fatalf("bad draft: %+v", sent[0])
	}
}

func TestComposeReplyPrefill(t *testing.T) {
	tr := seeded()
	c := paidClient(tr)
	m := readerModel{c: c, ctx: context.Background(), mailbox: Inbox}
	orig, _, _ := c.Read(context.Background(), Inbox, 1, true)
	m.current = orig
	m.mode = modeMessage
	m.startReply(false)
	if m.mode != modeCompose {
		t.Fatal("reply did not enter compose mode")
	}
	if m.compose.to != "carol@example.com" || m.compose.subject != "Re: Welcome" {
		t.Fatalf("reply prefill wrong: to=%q subject=%q", m.compose.to, m.compose.subject)
	}
	if m.compose.inReplyTo == "" || m.compose.focus != 3 {
		t.Fatalf("reply threading/focus wrong: %+v", m.compose)
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
