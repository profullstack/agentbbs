// Package mail sends transactional email (account confirmation) over SMTP.
// It is intentionally tiny: standard net/smtp with STARTTLS, configured from
// AGENTBBS_SMTP_* env vars. When unconfigured, Configured() reports false and
// the caller logs the confirmation link instead of sending it.
package mail

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"os"
	"strings"
)

// Config is an SMTP relay. Host+From are the minimum for Configured().
type Config struct {
	Host string // smtp server host (no port)
	Port string // default 587 (STARTTLS)
	User string // auth user; empty = no auth
	Pass string
	From string // envelope + From: header

	// ServerName is the name STARTTLS certificates are verified against when it
	// differs from Host. A co-located relay is dialled on loopback but presents a
	// certificate for its public mail host, never for 127.0.0.1. Mirrors
	// AGENTBBS_MAIL_SMTP_SERVERNAME on the mailbox gateway.
	ServerName string
}

// ConfigFromEnv reads AGENTBBS_SMTP_{HOST,PORT,USER,PASS,FROM,SERVERNAME}.
func ConfigFromEnv() Config {
	return Config{
		Host:       os.Getenv("AGENTBBS_SMTP_HOST"),
		Port:       os.Getenv("AGENTBBS_SMTP_PORT"),
		User:       os.Getenv("AGENTBBS_SMTP_USER"),
		Pass:       os.Getenv("AGENTBBS_SMTP_PASS"),
		From:       os.Getenv("AGENTBBS_SMTP_FROM"),
		ServerName: os.Getenv("AGENTBBS_SMTP_SERVERNAME"),
	}
}

// Configured reports whether email can actually be sent.
func (c Config) Configured() bool { return c.Host != "" && c.From != "" }

// port is the submission port, defaulting to STARTTLS 587.
func (c Config) port() string {
	if c.Port == "" {
		return "587"
	}
	return c.Port
}

// tlsServerName is the identity STARTTLS certificates are checked against.
// AGENTBBS_SMTP_SERVERNAME wins; otherwise the dialled host is used, which is
// only meaningful when that host is a real name.
func (c Config) tlsServerName() string {
	if c.ServerName != "" {
		return c.ServerName
	}
	return c.Host
}

// isLoopback reports whether the relay lives on this host. A loopback
// connection never leaves the box, so there is nothing for certificate
// verification to defend against — the same reasoning docs/mail.md already
// applies to the plaintext Dovecot hand-off.
func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// Send delivers a plain-text message. STARTTLS is negotiated whenever the
// server advertises it (the common case on :587 and on a co-located :25).
// Implicit-TLS :465 is not supported — use a STARTTLS port.
//
// Certificates are verified against tlsServerName() EXCEPT on a loopback relay,
// where verification is skipped deliberately. An on-box MTA serves a cert for
// its public mail host and renews it on its own schedule; making join@ signups
// depend on that cert being both name-matched and unexpired is how registration
// silently died for nine days when the Mailu cert lapsed. Loopback traffic is
// not interceptable, so the check bought nothing and cost everything.
func (c Config) Send(to, subject, body string) error {
	if !c.Configured() {
		return fmt.Errorf("smtp not configured")
	}
	addr := net.JoinHostPort(c.Host, c.port())
	msg := "From: " + c.From + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		strings.ReplaceAll(body, "\n", "\r\n") + "\r\n"

	cl, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("smtp dial %s: %w", addr, err)
	}
	defer func() { _ = cl.Close() }()

	if ok, _ := cl.Extension("STARTTLS"); ok {
		name := c.tlsServerName()
		conf := &tls.Config{ServerName: name}
		if isLoopback(c.Host) {
			conf = &tls.Config{ServerName: name, InsecureSkipVerify: true} // #nosec G402 -- loopback relay, see doc comment
		}
		if err := cl.StartTLS(conf); err != nil {
			return fmt.Errorf("smtp starttls %s (servername %q): %w", addr, name, err)
		}
	}
	if c.User != "" {
		if err := cl.Auth(smtp.PlainAuth("", c.User, c.Pass, c.tlsServerName())); err != nil {
			return fmt.Errorf("smtp auth %s: %w", addr, err)
		}
	}
	if err := cl.Mail(c.From); err != nil {
		return fmt.Errorf("smtp mail from %s: %w", c.From, err)
	}
	if err := cl.Rcpt(to); err != nil {
		return fmt.Errorf("smtp rcpt to %s: %w", to, err)
	}
	w, err := cl.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp close: %w", err)
	}
	return cl.Quit()
}
