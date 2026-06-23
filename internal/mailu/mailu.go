// Package mailu provisions member mailboxes on the self-hosted Mailu stack via
// its admin REST API. Every verified AgentBBS member gets a real mailbox at
// <name>@<domain> (e.g. alice@bbs.profullstack.com); the agentbbs gateway then
// opens it over IMAP with the Dovecot master user, so members never manage an
// IMAP/SMTP password. Mailbox creation is the one thing that must happen up
// front, which is what EnsureUser does (idempotently).
//
// The Mailu admin API listens on the loopback HTTP front (default
// http://127.0.0.1:8080) and authenticates with the token set as API_TOKEN in
// mailu.env. When no token is configured Configured() reports false and callers
// skip provisioning (the address is still shown).
//
// Config (env):
//
//	AGENTBBS_MAIL_ADMIN_URL   Mailu admin base URL (default http://127.0.0.1:8080)
//	AGENTBBS_MAIL_API_TOKEN   Mailu API token (from mailu.env API_TOKEN)
//	AGENTBBS_MAIL_QUOTA_BYTES  per-mailbox quota in bytes (default 1 GiB)
package mailu

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultQuotaBytes is the per-mailbox storage quota when unset (1 GiB).
const DefaultQuotaBytes = 1 << 30

// Config holds the Mailu admin-API endpoint and credentials.
type Config struct {
	BaseURL    string
	Token      string
	QuotaBytes int64
	HTTP       *http.Client
}

// ConfigFromEnv reads the Mailu admin settings from the environment.
func ConfigFromEnv() Config {
	q, _ := strconv.ParseInt(os.Getenv("AGENTBBS_MAIL_QUOTA_BYTES"), 10, 64)
	if q <= 0 {
		q = DefaultQuotaBytes
	}
	base := os.Getenv("AGENTBBS_MAIL_ADMIN_URL")
	if base == "" {
		base = "http://127.0.0.1:8080"
	}
	return Config{
		BaseURL:    strings.TrimRight(base, "/"),
		Token:      os.Getenv("AGENTBBS_MAIL_API_TOKEN"),
		QuotaBytes: q,
		HTTP:       &http.Client{Timeout: 15 * time.Second},
	}
}

// Client talks to the Mailu admin REST API.
type Client struct {
	cfg Config
}

// New builds a client. NewFromEnv is the usual entry point.
func New(cfg Config) *Client {
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 15 * time.Second}
	}
	if cfg.QuotaBytes <= 0 {
		cfg.QuotaBytes = DefaultQuotaBytes
	}
	return &Client{cfg: cfg}
}

// NewFromEnv builds a client from the environment.
func NewFromEnv() *Client { return New(ConfigFromEnv()) }

// Configured reports whether mailboxes can actually be provisioned.
func (c *Client) Configured() bool {
	return c != nil && c.cfg.Token != "" && c.cfg.BaseURL != ""
}

func (c *Client) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.BaseURL+"/api/v1"+path, rdr)
	if err != nil {
		return nil, err
	}
	// Mailu authenticates the admin API with the raw token in Authorization.
	req.Header.Set("Authorization", c.cfg.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.cfg.HTTP.Do(req)
}

// UserExists reports whether email already has a mailbox.
func (c *Client) UserExists(ctx context.Context, email string) (bool, error) {
	resp, err := c.do(ctx, http.MethodGet, "/user/"+email, nil)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode == http.StatusOK:
		return true, nil
	case resp.StatusCode == http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("mailu user lookup %s: %s", email, resp.Status)
	}
}

// EnsureUser creates email@... if it doesn't exist. It is idempotent: an
// existing mailbox (or an "already exists" create response) is success. The
// generated password is unused by members — the gateway master user opens every
// mailbox — but Mailu requires one at creation time.
func (c *Client) EnsureUser(ctx context.Context, localPart, domain string) error {
	if !c.Configured() {
		return fmt.Errorf("mailu not configured")
	}
	email := localPart + "@" + domain
	exists, err := c.UserExists(ctx, email)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	pw, err := randomPassword()
	if err != nil {
		return err
	}
	payload := map[string]any{
		"email":        email,
		"raw_password": pw,
		"comment":      "agentbbs member",
		"quota_bytes":  c.cfg.QuotaBytes,
		"enabled":      true,
	}
	resp, err := c.do(ctx, http.MethodPost, "/user", payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	// A concurrent create / pre-existing mailbox is fine.
	if resp.StatusCode == http.StatusConflict ||
		strings.Contains(strings.ToLower(string(b)), "already exists") {
		return nil
	}
	return fmt.Errorf("mailu create user %s: %s: %s", email, resp.Status, strings.TrimSpace(string(b)))
}

// SetPassword sets the mailbox password (so the member can log into webmail).
// The gateway opens mailboxes via the Dovecot master user and never needs this,
// but webmail (Roundcube) requires the member to have a known password.
func (c *Client) SetPassword(ctx context.Context, localPart, domain, password string) error {
	if !c.Configured() {
		return fmt.Errorf("mailu not configured")
	}
	email := localPart + "@" + domain
	resp, err := c.do(ctx, http.MethodPatch, "/user/"+email, map[string]any{"raw_password": password})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("mailu set password %s: %s: %s", email, resp.Status, strings.TrimSpace(string(b)))
}

func randomPassword() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
