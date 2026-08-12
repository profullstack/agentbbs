package mailbox

import (
	"context"
	"net"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

func newFakeIMAP(t *testing.T) string {
	t.Helper()

	backend := imapmemserver.New()
	user := imapmemserver.NewUser("alice", "secret")
	if err := user.Create(Inbox, nil); err != nil {
		t.Fatal(err)
	}
	if err := user.Create(Sent, nil); err != nil {
		t.Fatal(err)
	}
	backend.AddUser(user)

	server := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return backend.NewSession(), nil, nil
		},
		InsecureAuth: true,
		Caps: imap.CapSet{
			imap.CapIMAP4rev1: {},
			imap.CapIMAP4rev2: {},
		},
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
	})
	return listener.Addr().String()
}

func TestIMAPSendCopiesMessageToSent(t *testing.T) {
	smtpServer := newFakeSMTP(t)
	transport, err := NewIMAPTransport(IMAPConfig{
		IMAPAddr:  newFakeIMAP(t),
		SMTPAddr:  smtpServer.addr,
		Username:  "alice",
		Password:  "secret",
		Plaintext: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transport.Close() })

	_, err = transport.Send(context.Background(), "alice@example.test", Draft{
		To:      []Address{{Address: "bob@example.test"}},
		Subject: "Saved message",
		Text:    "message body",
	})
	if err != nil {
		t.Fatal(err)
	}

	messages, err := transport.ListMessages(context.Background(), ListOptions{Mailbox: Sent})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("Sent contains %d messages; want 1", len(messages))
	}
	if messages[0].Subject != "Saved message" {
		t.Fatalf("Sent subject = %q; want %q", messages[0].Subject, "Saved message")
	}
	message, found, err := transport.ReadMessage(context.Background(), Sent, messages[0].UID)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("saved message not found in Sent")
	}
	if message.Text != "message body" {
		t.Fatalf("Sent body = %q; want %q", message.Text, "message body")
	}
}
