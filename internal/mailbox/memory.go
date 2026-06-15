package mailbox

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"
)

// MemoryTransport is a complete, dependency-free Transport for tests, local
// development, and as the reference for what the IMAP/SMTP adapter must do.
type MemoryTransport struct {
	mu        sync.Mutex
	byMailbox map[string][]Message
	nextUID   uint32
}

// NewMemoryTransport returns an empty in-memory transport.
func NewMemoryTransport() *MemoryTransport {
	return &MemoryTransport{byMailbox: map[string][]Message{}, nextUID: 1}
}

// Add inserts a message, filling defaults, and returns the stored copy.
func (m *MemoryTransport) Add(msg Message) Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	if msg.UID == 0 {
		msg.UID = m.nextUID
	}
	if msg.UID >= m.nextUID {
		m.nextUID = msg.UID + 1
	}
	if msg.Date.IsZero() {
		msg.Date = time.Now().UTC()
	}
	if msg.MessageID == "" {
		msg.MessageID = fmt.Sprintf("<%s@memory.local>", randToken())
	}
	if msg.Snippet == "" {
		msg.Snippet = Snippet(msg.Text, 140)
	}
	msg.HasAttachments = len(msg.Attachments) > 0
	m.byMailbox[msg.Mailbox] = append(m.byMailbox[msg.Mailbox], msg)
	return msg
}

func (m *MemoryTransport) ListMailboxes(_ context.Context) ([]Mailbox, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Mailbox, 0, len(m.byMailbox))
	for path, list := range m.byMailbox {
		unseen := 0
		for _, msg := range list {
			if !msg.Seen {
				unseen++
			}
		}
		out = append(out, Mailbox{Name: path, Path: path, Total: len(list), Unseen: unseen})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (m *MemoryTransport) ListMessages(_ context.Context, opts ListOptions) ([]MessageSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := append([]Message{}, m.byMailbox[opts.Mailbox]...)
	sort.Slice(list, func(i, j int) bool { return list[i].Date.After(list[j].Date) })
	if opts.Limit > 0 && len(list) > opts.Limit {
		list = list[:opts.Limit]
	}
	return summaries(list), nil
}

func (m *MemoryTransport) ReadMessage(_ context.Context, mailbox string, uid uint32) (Message, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, msg := range m.byMailbox[mailbox] {
		if msg.UID == uid {
			return msg, true, nil
		}
	}
	return Message{}, false, nil
}

func (m *MemoryTransport) Search(_ context.Context, opts SearchOptions) ([]MessageSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q := strings.ToLower(opts.Query)
	var hits []Message
	boxes := []string{opts.Mailbox}
	if opts.Mailbox == "" {
		boxes = boxes[:0]
		for b := range m.byMailbox {
			boxes = append(boxes, b)
		}
	}
	for _, b := range boxes {
		for _, msg := range m.byMailbox[b] {
			hay := strings.ToLower(msg.Subject + " " + msg.From.Address + " " + msg.From.Name + " " + msg.Text)
			if strings.Contains(hay, q) {
				hits = append(hits, msg)
			}
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Date.After(hits[j].Date) })
	if opts.Limit > 0 && len(hits) > opts.Limit {
		hits = hits[:opts.Limit]
	}
	return summaries(hits), nil
}

func (m *MemoryTransport) Send(_ context.Context, from string, d Draft) (SendResult, error) {
	domain := "memory.local"
	if at := strings.LastIndexByte(from, '@'); at >= 0 {
		domain = from[at+1:]
	}
	id := fmt.Sprintf("<%s@%s>", randToken(), domain)
	var refs []string
	if d.InReplyTo != "" {
		refs = []string{d.InReplyTo}
	}
	m.Add(Message{
		MessageSummary: MessageSummary{Mailbox: Sent, From: Address{Address: from}, To: d.To, Subject: d.Subject, Seen: true},
		CC:             d.CC,
		Text:           d.Text,
		MessageID:      id,
		References:     refs,
	})
	return SendResult{MessageID: id}, nil
}

func (m *MemoryTransport) SetFlags(_ context.Context, mailbox string, uid uint32, fc FlagChange) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.byMailbox[mailbox]
	for i := range list {
		if list[i].UID == uid {
			if fc.Seen != nil {
				list[i].Seen = *fc.Seen
			}
			if fc.Flagged != nil {
				list[i].Flagged = *fc.Flagged
			}
			return nil
		}
	}
	return ErrNotFound
}

func (m *MemoryTransport) DeleteMessage(_ context.Context, mailbox string, uid uint32) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.byMailbox[mailbox]
	for i := range list {
		if list[i].UID == uid {
			m.byMailbox[mailbox] = append(list[:i], list[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

func (m *MemoryTransport) Close() error { return nil }

func summaries(list []Message) []MessageSummary {
	out := make([]MessageSummary, len(list))
	for i, msg := range list {
		s := msg.MessageSummary
		s.HasAttachments = len(msg.Attachments) > 0
		out[i] = s
	}
	return out
}

func randToken() string {
	const alpha = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 10)
	for i := range b {
		b[i] = alpha[rand.Intn(len(alpha))]
	}
	return string(b)
}
