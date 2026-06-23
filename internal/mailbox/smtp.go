package mailbox

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// buildRFC822 renders a draft as an RFC 5322 message (CRLF line endings, UTF-8
// text body). It returns the bytes and the generated Message-ID.
func buildRFC822(from string, d Draft) ([]byte, string) {
	domain := "localhost"
	if at := strings.LastIndexByte(from, '@'); at >= 0 {
		domain = from[at+1:]
	}
	msgID := fmt.Sprintf("<%s@%s>", randToken(), domain)

	var b bytes.Buffer
	wh := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	wh("From", from)
	wh("To", headerAddrs(d.To))
	if len(d.CC) > 0 {
		wh("Cc", headerAddrs(d.CC))
	}
	wh("Subject", mime.QEncoding.Encode("utf-8", d.Subject))
	wh("Date", time.Now().Format(time.RFC1123Z))
	wh("Message-Id", msgID)
	if d.InReplyTo != "" {
		wh("In-Reply-To", d.InReplyTo)
		wh("References", d.InReplyTo)
	}
	wh("MIME-Version", "1.0")
	wh("Content-Type", "text/plain; charset=utf-8")
	wh("Content-Transfer-Encoding", "8bit")
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(d.Text, "\n", "\r\n"))
	return b.Bytes(), msgID
}

// headerAddrs renders an address list for a header, encoding display names.
func headerAddrs(list []Address) string {
	parts := make([]string, len(list))
	for i, a := range list {
		if a.Name == "" {
			parts[i] = a.Address
		} else {
			parts[i] = fmt.Sprintf("%s <%s>", mime.QEncoding.Encode("utf-8", a.Name), a.Address)
		}
	}
	return strings.Join(parts, ", ")
}

// recipients flattens To+Cc+Bcc into envelope recipient addresses.
func recipients(d Draft) []string {
	var out []string
	for _, l := range [][]Address{d.To, d.CC, d.BCC} {
		for _, a := range l {
			out = append(out, a.Address)
		}
	}
	return out
}

// smtpSend submits a built message via SMTP. It does STARTTLS when the server
// offers it, verifying the certificate against serverName (or the dial host when
// serverName is empty) — this lets us dial a trusted local relay by IP/loopback
// while still validating its real hostname certificate. A non-empty user adds
// AUTH; an empty user sends unauthenticated, for a relay that trusts the source.
//
// This mirrors net/smtp.SendMail but with an overridable TLS ServerName, which
// SendMail does not support (it pins ServerName to the dial host).
func smtpSend(addr, serverName, user, pass, from string, rcpts []string, msg []byte) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("smtp addr %q: %w", addr, err)
	}
	if serverName == "" {
		serverName = host
	}
	c, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("smtp dial %s: %w", addr, err)
	}
	defer func() { _ = c.Close() }()
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: serverName}); err != nil {
			return fmt.Errorf("smtp starttls (%s): %w", serverName, err)
		}
	}
	if user != "" {
		if err := c.Auth(smtp.PlainAuth("", user, pass, serverName)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	for _, rcpt := range rcpts {
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("smtp rcpt %s: %w", rcpt, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp close: %w", err)
	}
	return c.Quit()
}
