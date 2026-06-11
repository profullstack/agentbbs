// Package mail sends transactional email (account confirmation) over SMTP.
// It is intentionally tiny: standard net/smtp with STARTTLS, configured from
// AGENTBBS_SMTP_* env vars. When unconfigured, Configured() reports false and
// the caller logs the confirmation link instead of sending it.
package mail

import (
	"fmt"
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
}

// ConfigFromEnv reads AGENTBBS_SMTP_{HOST,PORT,USER,PASS,FROM}.
func ConfigFromEnv() Config {
	return Config{
		Host: os.Getenv("AGENTBBS_SMTP_HOST"),
		Port: os.Getenv("AGENTBBS_SMTP_PORT"),
		User: os.Getenv("AGENTBBS_SMTP_USER"),
		Pass: os.Getenv("AGENTBBS_SMTP_PASS"),
		From: os.Getenv("AGENTBBS_SMTP_FROM"),
	}
}

// Configured reports whether email can actually be sent.
func (c Config) Configured() bool { return c.Host != "" && c.From != "" }

// Send delivers a plain-text message. net/smtp negotiates STARTTLS when the
// server advertises it (the common case on :587). Implicit-TLS :465 is not
// supported — use a STARTTLS port.
func (c Config) Send(to, subject, body string) error {
	if !c.Configured() {
		return fmt.Errorf("smtp not configured")
	}
	port := c.Port
	if port == "" {
		port = "587"
	}
	var auth smtp.Auth
	if c.User != "" {
		auth = smtp.PlainAuth("", c.User, c.Pass, c.Host)
	}
	msg := "From: " + c.From + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		strings.ReplaceAll(body, "\n", "\r\n") + "\r\n"
	return smtp.SendMail(c.Host+":"+port, auth, c.From, []string{to}, []byte(msg))
}
