package mailbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	gomail "github.com/emersion/go-message/mail"
)

// IMAPConfig connects the adapter to the mail backend (Mailu: Dovecot IMAP +
// Postfix submission). Username/Password are the resolved login (which may be a
// Dovecot master-user login like "alice*gateway").
type IMAPConfig struct {
	IMAPAddr string // host:port, e.g. mail.profullstack.com:993 (implicit TLS)
	SMTPAddr string // host:port, e.g. smtp.profullstack.com:587 (STARTTLS)
	Username string
	Password string
	// SMTPUser/SMTPPass default to Username/Password when empty.
	SMTPUser string
	SMTPPass string
	// Plaintext dials IMAP without TLS. Used only for a co-located backend over
	// loopback (the Mailu gateway hitting Dovecot directly on 127.0.0.1, bypassing
	// the front's auth proxy so master-user login works) — the password never
	// leaves the host. Never enable it for a remote server.
	Plaintext bool
}

// imapTransport is a Transport backed by a single authenticated IMAP connection
// plus SMTP submission. Commands are serialized (the IMAP client is not safe for
// concurrent in-flight commands).
type imapTransport struct {
	cfg      IMAPConfig
	mu       sync.Mutex
	c        *imapclient.Client
	selected string
}

// NewIMAPTransport dials the IMAP server, logs in, and returns a Transport.
func NewIMAPTransport(cfg IMAPConfig) (Transport, error) {
	dial := imapclient.DialTLS
	if cfg.Plaintext {
		dial = imapclient.DialInsecure
	}
	c, err := dial(cfg.IMAPAddr, nil)
	if err != nil {
		return nil, fmt.Errorf("imap dial %s: %w", cfg.IMAPAddr, err)
	}
	if err := c.Login(cfg.Username, cfg.Password).Wait(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("imap login: %w", err)
	}
	return &imapTransport{cfg: cfg, c: c}, nil
}

// VerifyLogin checks a username/password against the IMAP backend by logging in
// and immediately logging out. It returns nil only when the credentials are
// accepted. The web file browser uses this to authenticate members with their
// webmail (Mailu/Dovecot) password — the same credential Roundcube uses.
func VerifyLogin(addr, user, pass string, plaintext bool) error {
	dial := imapclient.DialTLS
	if plaintext {
		dial = imapclient.DialInsecure
	}
	c, err := dial(addr, nil)
	if err != nil {
		return fmt.Errorf("imap dial %s: %w", addr, err)
	}
	defer func() { _ = c.Close() }()
	if err := c.Login(user, pass).Wait(); err != nil {
		return fmt.Errorf("imap login: %w", err)
	}
	_ = c.Logout().Wait()
	return nil
}

func (t *imapTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.c == nil {
		return nil
	}
	_ = t.c.Logout().Wait()
	return t.c.Close()
}

func (t *imapTransport) selectMailbox(name string, readOnly bool) (*imap.SelectData, error) {
	data, err := t.c.Select(name, &imap.SelectOptions{ReadOnly: readOnly}).Wait()
	if err != nil {
		return nil, fmt.Errorf("select %s: %w", name, err)
	}
	t.selected = name
	return data, nil
}

func (t *imapTransport) ListMailboxes(_ context.Context) ([]Mailbox, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	entries, err := t.c.List("", "*", nil).Collect()
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	out := make([]Mailbox, 0, len(entries))
	for _, e := range entries {
		mb := Mailbox{Name: e.Mailbox, Path: e.Mailbox}
		st, err := t.c.Status(e.Mailbox, &imap.StatusOptions{NumMessages: true, NumUnseen: true}).Wait()
		if err == nil {
			if st.NumMessages != nil {
				mb.Total = int(*st.NumMessages)
			}
			if st.NumUnseen != nil {
				mb.Unseen = int(*st.NumUnseen)
			}
		}
		out = append(out, mb)
	}
	return out, nil
}

func (t *imapTransport) ListMessages(_ context.Context, opts ListOptions) ([]MessageSummary, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	sel, err := t.selectMailbox(opts.Mailbox, true)
	if err != nil {
		return nil, err
	}
	n := sel.NumMessages
	if n == 0 {
		return nil, nil
	}
	start := uint32(1)
	if opts.Limit > 0 && n > uint32(opts.Limit) {
		start = n - uint32(opts.Limit) + 1
	}
	seq := imap.SeqSet(nil)
	seq.AddRange(start, n)
	bufs, err := t.c.Fetch(seq, &imap.FetchOptions{Envelope: true, Flags: true, UID: true}).Collect()
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	rows := make([]MessageSummary, 0, len(bufs))
	for _, b := range bufs {
		rows = append(rows, summaryFromBuf(opts.Mailbox, b))
	}
	// newest first
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return rows, nil
}

func (t *imapTransport) ReadMessage(_ context.Context, mailbox string, uid uint32) (Message, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, err := t.selectMailbox(mailbox, false); err != nil {
		return Message{}, false, err
	}
	set := imap.UIDSetNum(imap.UID(uid))
	section := &imap.FetchItemBodySection{}
	bufs, err := t.c.Fetch(set, &imap.FetchOptions{
		Envelope:    true,
		Flags:       true,
		UID:         true,
		BodySection: []*imap.FetchItemBodySection{section},
	}).Collect()
	if err != nil {
		return Message{}, false, fmt.Errorf("fetch uid %d: %w", uid, err)
	}
	if len(bufs) == 0 {
		return Message{}, false, nil
	}
	b := bufs[0]
	msg := Message{MessageSummary: summaryFromBuf(mailbox, b)}
	raw := b.FindBodySection(section)
	if len(raw) > 0 {
		fillBody(&msg, raw)
	}
	msg.Snippet = Snippet(msg.Text, 140)
	return msg, true, nil
}

func (t *imapTransport) Search(_ context.Context, opts SearchOptions) ([]MessageSummary, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	mailbox := opts.Mailbox
	if mailbox == "" {
		mailbox = Inbox
	}
	if _, err := t.selectMailbox(mailbox, true); err != nil {
		return nil, err
	}
	data, err := t.c.UIDSearch(&imap.SearchCriteria{Text: []string{opts.Query}}, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	uids := data.AllUIDs()
	if len(uids) == 0 {
		return nil, nil
	}
	if opts.Limit > 0 && len(uids) > opts.Limit {
		uids = uids[len(uids)-opts.Limit:]
	}
	bufs, err := t.c.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{Envelope: true, Flags: true, UID: true}).Collect()
	if err != nil {
		return nil, fmt.Errorf("search fetch: %w", err)
	}
	rows := make([]MessageSummary, 0, len(bufs))
	for _, b := range bufs {
		rows = append(rows, summaryFromBuf(mailbox, b))
	}
	return rows, nil
}

func (t *imapTransport) Send(_ context.Context, from string, d Draft) (SendResult, error) {
	msg, msgID := buildRFC822(from, d)
	// SMTPUser may be empty for a trusted local relay (no AUTH).
	if err := smtpSend(t.cfg.SMTPAddr, t.cfg.SMTPUser, t.cfg.SMTPPass, from, recipients(d), msg); err != nil {
		return SendResult{}, fmt.Errorf("smtp send: %w", err)
	}
	// Best-effort copy to Sent so the message shows in the member's mailbox.
	t.mu.Lock()
	_, _ = t.c.Append(Sent, int64(len(msg)), nil).Wait() // ignore APPEND errors
	t.mu.Unlock()
	return SendResult{MessageID: msgID}, nil
}

func (t *imapTransport) SetFlags(_ context.Context, mailbox string, uid uint32, fc FlagChange) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, err := t.selectMailbox(mailbox, false); err != nil {
		return err
	}
	set := imap.UIDSetNum(imap.UID(uid))
	apply := func(flag imap.Flag, on bool) error {
		op := imap.StoreFlagsDel
		if on {
			op = imap.StoreFlagsAdd
		}
		return t.c.Store(set, &imap.StoreFlags{Op: op, Flags: []imap.Flag{flag}, Silent: true}, nil).Close()
	}
	if fc.Seen != nil {
		if err := apply(imap.FlagSeen, *fc.Seen); err != nil {
			return fmt.Errorf("store seen: %w", err)
		}
	}
	if fc.Flagged != nil {
		if err := apply(imap.FlagFlagged, *fc.Flagged); err != nil {
			return fmt.Errorf("store flagged: %w", err)
		}
	}
	return nil
}

func (t *imapTransport) DeleteMessage(_ context.Context, mailbox string, uid uint32) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, err := t.selectMailbox(mailbox, false); err != nil {
		return err
	}
	set := imap.UIDSetNum(imap.UID(uid))
	if err := t.c.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagDeleted}, Silent: true}, nil).Close(); err != nil {
		return fmt.Errorf("store deleted: %w", err)
	}
	if err := t.c.Expunge().Close(); err != nil {
		return fmt.Errorf("expunge: %w", err)
	}
	return nil
}

func summaryFromBuf(mailbox string, b *imapclient.FetchMessageBuffer) MessageSummary {
	s := MessageSummary{Mailbox: mailbox, UID: uint32(b.UID)}
	if b.Envelope != nil {
		env := b.Envelope
		s.Subject = env.Subject
		s.Date = env.Date
		s.From = firstAddr(env.From)
		s.To = addrs(env.To)
	}
	for _, f := range b.Flags {
		switch f {
		case imap.FlagSeen:
			s.Seen = true
		case imap.FlagFlagged:
			s.Flagged = true
		}
	}
	return s
}

func firstAddr(list []imap.Address) Address {
	if len(list) == 0 {
		return Address{}
	}
	return imapAddr(list[0])
}

func addrs(list []imap.Address) []Address {
	out := make([]Address, 0, len(list))
	for _, a := range list {
		out = append(out, imapAddr(a))
	}
	return out
}

func imapAddr(a imap.Address) Address {
	addr := a.Mailbox
	if a.Host != "" {
		addr = a.Mailbox + "@" + a.Host
	}
	return Address{Name: a.Name, Address: addr}
}

// fillBody parses the raw RFC822 message into the text/html body + attachment
// metadata using go-message's mail reader.
func fillBody(msg *Message, raw []byte) {
	mr, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		// Not MIME we can parse — keep the raw text after the header break.
		msg.Text = rawBody(raw)
		return
	}
	if env := mr.Header; env.Get("Message-Id") != "" {
		msg.MessageID = env.Get("Message-Id")
	}
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		switch h := p.Header.(type) {
		case *gomail.InlineHeader:
			body, _ := io.ReadAll(p.Body)
			ct, _, _ := h.ContentType()
			if strings.EqualFold(ct, "text/html") {
				msg.HTML = string(body)
			} else if msg.Text == "" {
				msg.Text = string(body)
			}
		case *gomail.AttachmentHeader:
			name, _ := h.Filename()
			ct, _, _ := h.ContentType()
			body, _ := io.ReadAll(p.Body)
			msg.Attachments = append(msg.Attachments, Attachment{Filename: name, ContentType: ct, Size: len(body)})
		}
	}
	msg.HasAttachments = len(msg.Attachments) > 0
}

// rawBody returns the body after the first blank line of a raw RFC822 message.
func rawBody(raw []byte) string {
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		return string(raw[i+4:])
	}
	if i := bytes.Index(raw, []byte("\n\n")); i >= 0 {
		return string(raw[i+2:])
	}
	return string(raw)
}
