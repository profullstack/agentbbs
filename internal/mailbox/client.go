package mailbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Identity is the acting member and whether they hold the paid membership.
type Identity struct {
	Name string // local-part / handle, e.g. "alice"
	Paid bool   // Founding Lifetime Member; mail is gated on this
}

// ErrNotPaid is returned to a non-paid member attempting a mail action.
var ErrNotPaid = errors.New("AgentMail is a Founding Lifetime Member feature ($99 one-time) — upgrade: ssh join@bbs.profullstack.com")

// Client is the ergonomic, paid-gated facade the TUI and bot mode use. Every
// method returns plain structs, so the same calls serve humans and agents.
type Client struct {
	t        Transport
	id       Identity
	domain   string
	pageSize int
}

// NewClient builds a paid-gated client. domain is the mail domain (e.g.
// mail.profullstack.com); pageSize defaults to 50 when <= 0.
func NewClient(t Transport, id Identity, domain string, pageSize int) *Client {
	if pageSize <= 0 {
		pageSize = 50
	}
	return &Client{t: t, id: id, domain: domain, pageSize: pageSize}
}

// Address is the member's own mailbox address, e.g. alice@mail.profullstack.com.
func (c *Client) Address() string { return c.id.Name + "@" + c.domain }

func (c *Client) gate() error {
	if c.id.Name == "" || !c.id.Paid {
		return ErrNotPaid
	}
	return nil
}

// Mailboxes lists folders with unread/total counts.
func (c *Client) Mailboxes(ctx context.Context) ([]Mailbox, error) {
	if err := c.gate(); err != nil {
		return nil, err
	}
	return c.t.ListMailboxes(ctx)
}

// List returns newest-first summaries for a mailbox (INBOX when empty).
func (c *Client) List(ctx context.Context, mailbox string, limit int) ([]MessageSummary, error) {
	if err := c.gate(); err != nil {
		return nil, err
	}
	if mailbox == "" {
		mailbox = Inbox
	}
	if limit <= 0 {
		limit = c.pageSize
	}
	return c.t.ListMessages(ctx, ListOptions{Mailbox: mailbox, Limit: limit})
}

// Read fetches a full message, marking it seen unless peek is true.
func (c *Client) Read(ctx context.Context, mailbox string, uid uint32, peek bool) (Message, bool, error) {
	if err := c.gate(); err != nil {
		return Message{}, false, err
	}
	msg, ok, err := c.t.ReadMessage(ctx, mailbox, uid)
	if err != nil || !ok {
		return msg, ok, err
	}
	if !peek && !msg.Seen {
		seen := true
		if err := c.t.SetFlags(ctx, mailbox, uid, FlagChange{Seen: &seen}); err == nil {
			msg.Seen = true
		}
	}
	return msg, true, nil
}

// Search runs a free-text search across a mailbox (or all when empty).
func (c *Client) Search(ctx context.Context, query, mailbox string, limit int) ([]MessageSummary, error) {
	if err := c.gate(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = c.pageSize
	}
	return c.t.Search(ctx, SearchOptions{Query: query, Mailbox: mailbox, Limit: limit})
}

// Send validates, stamps From, and sends a draft.
func (c *Client) Send(ctx context.Context, d Draft) (SendResult, error) {
	if err := c.gate(); err != nil {
		return SendResult{}, err
	}
	norm, err := NormalizeDraft(d)
	if err != nil {
		return SendResult{}, err
	}
	return c.t.Send(ctx, c.Address(), norm)
}

// Reply addresses the original sender (and, when replyAll, the other recipients
// minus the member), prefixes "Re:", threads via In-Reply-To, and sends.
func (c *Client) Reply(ctx context.Context, orig Message, text string, replyAll bool) (SendResult, error) {
	if err := c.gate(); err != nil {
		return SendResult{}, err
	}
	self := strings.ToLower(c.Address())
	to := orig.From
	if orig.ReplyTo != nil {
		to = *orig.ReplyTo
	}
	var cc []Address
	if replyAll {
		for _, a := range append(append([]Address{}, orig.To...), orig.CC...) {
			la := strings.ToLower(a.Address)
			if la != self && la != strings.ToLower(to.Address) {
				cc = append(cc, a)
			}
		}
	}
	subject := orig.Subject
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}
	return c.Send(ctx, Draft{To: []Address{to}, CC: cc, Subject: subject, Text: text, InReplyTo: orig.MessageID})
}

// Flag sets or clears the \Flagged flag.
func (c *Client) Flag(ctx context.Context, mailbox string, uid uint32, flagged bool) error {
	if err := c.gate(); err != nil {
		return err
	}
	return c.t.SetFlags(ctx, mailbox, uid, FlagChange{Flagged: &flagged})
}

// MarkSeen sets or clears the \Seen flag.
func (c *Client) MarkSeen(ctx context.Context, mailbox string, uid uint32, seen bool) error {
	if err := c.gate(); err != nil {
		return err
	}
	return c.t.SetFlags(ctx, mailbox, uid, FlagChange{Seen: &seen})
}

// Delete removes a message.
func (c *Client) Delete(ctx context.Context, mailbox string, uid uint32) error {
	if err := c.gate(); err != nil {
		return err
	}
	return c.t.DeleteMessage(ctx, mailbox, uid)
}

// Close releases the underlying transport.
func (c *Client) Close() error {
	if c.t == nil {
		return nil
	}
	return c.t.Close()
}

// helper used by bot mode to surface a friendly "not found" message.
func notFound(mailbox string, uid uint32) error {
	return fmt.Errorf("%w: %s/%d", ErrNotFound, mailbox, uid)
}
