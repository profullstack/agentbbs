// Package forwardemail provisions members' personal @bbs email addresses by
// creating aliases on forwardemail.net (https://forwardemail.net) via its REST
// API. A premium member gets <username>@<domain> forwarded to the real email
// they verified at join@. When unconfigured (no API key) Configured() reports
// false and callers just display the address without creating it.
//
// Config (env):
//
//	AGENTBBS_FORWARDEMAIL_API_KEY   forwardemail.net API key (HTTP basic user)
//	AGENTBBS_FORWARDEMAIL_DOMAIN    alias domain (defaults to the BBS host)
//	AGENTBBS_WEBMAIL_URL            webmail interface URL shown to members
package forwardemail

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const apiBase = "https://api.forwardemail.net/v1"

// Config holds the forwardemail.net credentials and the alias domain.
type Config struct {
	APIKey  string
	Domain  string
	Webmail string
}

// ConfigFromEnv reads the forwardemail settings from the environment.
func ConfigFromEnv() Config {
	return Config{
		APIKey:  os.Getenv("AGENTBBS_FORWARDEMAIL_API_KEY"),
		Domain:  os.Getenv("AGENTBBS_FORWARDEMAIL_DOMAIN"),
		Webmail: os.Getenv("AGENTBBS_WEBMAIL_URL"),
	}
}

// Configured reports whether aliases can actually be created.
func (c Config) Configured() bool { return c.APIKey != "" && c.Domain != "" }

// WebmailURL is the webmail interface members use to read their mail (may be "").
func (c Config) WebmailURL() string { return c.Webmail }

// Address is the personal email for a username, e.g. alice@bbs.profullstack.com.
func (c Config) Address(localPart string) string { return localPart + "@" + c.Domain }

// CreateAlias creates (or confirms) localPart@Domain forwarding to recipient.
// It is idempotent: an "already exists" response is treated as success.
func (c Config) CreateAlias(localPart, recipient string) error {
	if !c.Configured() {
		return fmt.Errorf("forwardemail not configured")
	}
	form := url.Values{
		"name":       {localPart},
		"recipients": {recipient},
		"is_enabled": {"true"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	endpoint := apiBase + "/domains/" + url.PathEscape(c.Domain) + "/aliases"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	// forwardemail uses HTTP basic auth with the API key as the username and an
	// empty password.
	req.SetBasicAuth(c.APIKey, "")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	// Re-running for an existing member is normal — don't treat it as an error.
	if strings.Contains(strings.ToLower(string(body)), "already exists") {
		return nil
	}
	return fmt.Errorf("forwardemail create alias: %s: %s", resp.Status, strings.TrimSpace(string(body)))
}
