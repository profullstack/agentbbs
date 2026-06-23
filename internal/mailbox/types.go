// Package mailbox is the BBS-side mail client for members (a free benefit of
// membership): a transport-agnostic core (read, search, compose, send, flag,
// delete) with a Bubble Tea TUI for humans and a line-oriented JSON mode for
// agents/bots. Addresses are <name>@bbs.profullstack.com; it talks to the
// self-hosted Mailu stack (Dovecot IMAP + Postfix submission) hosted on
// mail.profullstack.com.
//
// The TS counterpart is @logicsrc/plugin-agentmail; the domain shapes here are
// deliberately the same so tooling can move between them.
package mailbox

import "time"

// Address is a parsed mail address.
type Address struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
}

// Mailbox is an IMAP folder with unread/total counts.
type Mailbox struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Unseen int    `json:"unseen"`
	Total  int    `json:"total"`
}

// MessageSummary is a lightweight row for list/search views.
type MessageSummary struct {
	UID            uint32    `json:"uid"`
	Mailbox        string    `json:"mailbox"`
	From           Address   `json:"from"`
	To             []Address `json:"to"`
	Subject        string    `json:"subject"`
	Date           time.Time `json:"date"`
	Seen           bool      `json:"seen"`
	Flagged        bool      `json:"flagged"`
	HasAttachments bool      `json:"hasAttachments"`
	Snippet        string    `json:"snippet"`
}

// Attachment is attachment metadata (bytes fetched separately).
type Attachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Size        int    `json:"size"`
}

// Message is a fully fetched message.
type Message struct {
	MessageSummary
	CC          []Address    `json:"cc,omitempty"`
	ReplyTo     *Address     `json:"replyTo,omitempty"`
	MessageID   string       `json:"messageId"`
	References  []string     `json:"references,omitempty"`
	Text        string       `json:"text"`
	HTML        string       `json:"html,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

// Draft is an outgoing message.
type Draft struct {
	To        []Address `json:"to"`
	CC        []Address `json:"cc,omitempty"`
	BCC       []Address `json:"bcc,omitempty"`
	Subject   string    `json:"subject"`
	Text      string    `json:"text"`
	InReplyTo string    `json:"inReplyTo,omitempty"`
}

// ListOptions controls a mailbox listing.
type ListOptions struct {
	Mailbox string
	Limit   int
}

// SearchOptions controls a search.
type SearchOptions struct {
	Mailbox string // empty = all mailboxes
	Query   string
	Limit   int
}

// FlagChange is a partial flag update; nil fields are left unchanged.
type FlagChange struct {
	Seen    *bool
	Flagged *bool
}

// SendResult reports a sent message's id.
type SendResult struct {
	MessageID string `json:"messageId"`
}

const (
	// Inbox is the default mailbox.
	Inbox = "INBOX"
	// Sent is where sent mail is stored.
	Sent = "Sent"
)
