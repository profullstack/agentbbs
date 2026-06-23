package mailbox

import (
	"bufio"
	"net"
	"strings"
	"sync"
	"testing"
)

// fakeSMTP is a minimal SMTP server (no STARTTLS advertised) that records the
// envelope and body of one delivered message, so we can exercise smtpSend's
// dial→MAIL→RCPT→DATA→QUIT flow without real TLS.
type fakeSMTP struct {
	addr        string
	ln          net.Listener
	mu          sync.Mutex
	from        string
	rcpts       []string
	body        strings.Builder
	gotMailFrom bool
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{addr: ln.Addr().String(), ln: ln}
	go f.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

func (f *fakeSMTP) serve() {
	conn, err := f.ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	w := func(s string) { _, _ = conn.Write([]byte(s)) }
	w("220 mock ESMTP\r\n")
	inData := false
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		if inData {
			if strings.TrimRight(line, "\r\n") == "." {
				inData = false
				w("250 ok\r\n")
				continue
			}
			f.mu.Lock()
			f.body.WriteString(line)
			f.mu.Unlock()
			continue
		}
		up := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(up, "EHLO"), strings.HasPrefix(up, "HELO"):
			w("250 mock\r\n") // single line => no extensions (no STARTTLS)
		case strings.HasPrefix(up, "MAIL FROM"):
			f.mu.Lock()
			f.from = strings.TrimSpace(line[len("MAIL FROM:"):])
			f.gotMailFrom = true
			f.mu.Unlock()
			w("250 ok\r\n")
		case strings.HasPrefix(up, "RCPT TO"):
			f.mu.Lock()
			f.rcpts = append(f.rcpts, strings.TrimSpace(line[len("RCPT TO:"):]))
			f.mu.Unlock()
			w("250 ok\r\n")
		case strings.HasPrefix(up, "DATA"):
			inData = true
			w("354 go ahead\r\n")
		case strings.HasPrefix(up, "QUIT"):
			w("221 bye\r\n")
			return
		default:
			w("250 ok\r\n")
		}
	}
}

func TestSMTPSendFlow(t *testing.T) {
	srv := newFakeSMTP(t)

	msg := []byte("Subject: hi\r\n\r\nbody text\r\n")
	err := smtpSend(srv.addr, "", "", "", "alice@bbs.test", []string{"bob@example.com", "carol@example.com"}, msg)
	if err != nil {
		t.Fatalf("smtpSend: %v", err)
	}

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if !srv.gotMailFrom || !strings.Contains(srv.from, "alice@bbs.test") {
		t.Fatalf("MAIL FROM wrong: %q", srv.from)
	}
	if len(srv.rcpts) != 2 {
		t.Fatalf("want 2 recipients, got %v", srv.rcpts)
	}
	if !strings.Contains(srv.body.String(), "body text") {
		t.Fatalf("body not delivered: %q", srv.body.String())
	}
}

func TestSMTPSendBadAddr(t *testing.T) {
	if err := smtpSend("not-a-host-port", "", "", "", "a@b", []string{"c@d"}, []byte("x")); err == nil {
		t.Fatal("expected error for malformed addr")
	}
}
