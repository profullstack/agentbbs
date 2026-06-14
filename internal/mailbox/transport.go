package mailbox

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Transport is the low-level mailbox backend. The TUI, the bot mode, and the
// Client all talk to this interface, so an in-memory fake (tests/dev) and the
// real IMAP/SMTP adapter are interchangeable.
type Transport interface {
	ListMailboxes(ctx context.Context) ([]Mailbox, error)
	ListMessages(ctx context.Context, opts ListOptions) ([]MessageSummary, error)
	// ReadMessage returns (Message, false, nil) when the uid is unknown.
	ReadMessage(ctx context.Context, mailbox string, uid uint32) (Message, bool, error)
	Search(ctx context.Context, opts SearchOptions) ([]MessageSummary, error)
	// Send delivers an already-validated draft, stamped from `from`.
	Send(ctx context.Context, from string, d Draft) (SendResult, error)
	SetFlags(ctx context.Context, mailbox string, uid uint32, fc FlagChange) error
	DeleteMessage(ctx context.Context, mailbox string, uid uint32) error
	Close() error
}

// ErrNotFound is returned when a uid/mailbox cannot be resolved.
var ErrNotFound = errors.New("mailbox: message not found")

var addrRe = regexp.MustCompile(`^(.*)<([^>]+)>\s*$`)

// ParseAddress parses "Name <a@b>" or a bare "a@b".
func ParseAddress(raw string) Address {
	s := strings.TrimSpace(raw)
	if m := addrRe.FindStringSubmatch(s); m != nil {
		name := strings.Trim(strings.TrimSpace(m[1]), `"`)
		return Address{Name: strings.TrimSpace(name), Address: strings.TrimSpace(m[2])}
	}
	return Address{Address: strings.Trim(s, "<>")}
}

// FormatAddress renders an Address back to "Name <addr>" (or just the address).
func FormatAddress(a Address) string {
	if a.Name == "" {
		return a.Address
	}
	if strings.ContainsAny(a.Name, `",<>@`) {
		return fmt.Sprintf("%q <%s>", a.Name, a.Address)
	}
	return fmt.Sprintf("%s <%s>", a.Name, a.Address)
}

// Snippet collapses whitespace and truncates to at most max runes.
func Snippet(body string, max int) string {
	flat := strings.Join(strings.Fields(body), " ")
	if max <= 0 || len([]rune(flat)) <= max {
		return flat
	}
	r := []rune(flat)
	return string(r[:max-1]) + "…"
}

// ValidEmail is a loose RFC5322-ish check: one @, a dotted domain, no spaces.
func ValidEmail(v string) bool {
	v = strings.TrimSpace(v)
	if len(v) < 3 || len(v) > 254 || strings.ContainsAny(v, " \t\r\n") {
		return false
	}
	at := strings.LastIndexByte(v, '@')
	if at <= 0 || at == len(v)-1 {
		return false
	}
	return strings.Contains(v[at+1:], ".")
}

// NormalizeDraft trims the subject, drops empty recipient lists, and rejects a
// draft with no valid recipient.
func NormalizeDraft(d Draft) (Draft, error) {
	clean := func(in []Address) []Address {
		out := make([]Address, 0, len(in))
		for _, a := range in {
			a.Address = strings.TrimSpace(a.Address)
			if a.Address != "" {
				out = append(out, a)
			}
		}
		return out
	}
	to, cc, bcc := clean(d.To), clean(d.CC), clean(d.BCC)
	all := append(append(append([]Address{}, to...), cc...), bcc...)
	if len(all) == 0 {
		return Draft{}, errors.New("a draft needs at least one recipient")
	}
	for _, a := range all {
		if !ValidEmail(a.Address) {
			return Draft{}, fmt.Errorf("invalid recipient address: %s", a.Address)
		}
	}
	return Draft{To: to, CC: cc, BCC: bcc, Subject: strings.TrimSpace(d.Subject), Text: d.Text, InReplyTo: d.InReplyTo}, nil
}
