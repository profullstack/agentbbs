// Package forgejo provisions a git.profullstack.com account for every verified
// AgentBBS member (free and paid alike) on the self-hosted Forgejo backend that
// powers AgentGit. BBS membership *is* the git account: when a member verifies
// their email, EnsureUser creates the matching Forgejo user. It is idempotent —
// an existing account is left untouched. When unconfigured (no admin token)
// Configured() reports false and callers skip provisioning entirely.
//
// This mirrors the AgentGit ForgejoAdapter.ensureUser contract in
// profullstack/logicsrc (plugins/agentgit). Plan (free vs. premium) never
// affects whether the account exists — only quotas, which are enforced
// server-side by AgentGit merge policy, not here.
//
// Config (env):
//
//	AGENTBBS_FORGEJO_URL          base URL, e.g. https://git.profullstack.com
//	AGENTBBS_FORGEJO_ADMIN_TOKEN  Forgejo admin access token
package forgejo

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
	"strings"
	"time"
)

// Config holds the Forgejo base URL and admin token.
type Config struct {
	BaseURL string
	Token   string
}

// ConfigFromEnv reads the Forgejo settings from the environment.
func ConfigFromEnv() Config {
	return Config{
		BaseURL: strings.TrimRight(os.Getenv("AGENTBBS_FORGEJO_URL"), "/"),
		Token:   os.Getenv("AGENTBBS_FORGEJO_ADMIN_TOKEN"),
	}
}

// Configured reports whether accounts can actually be provisioned.
func (c Config) Configured() bool { return c.BaseURL != "" && c.Token != "" }

// EnsureUser creates a Forgejo account for username (forwarding to email) if it
// does not already exist. It is idempotent: created is false when the account
// was already present. New accounts are created with must_change_password — git
// access is via SSH keys, so the generated password is never used interactively.
func (c Config) EnsureUser(username, email string) (created bool, err error) {
	if !c.Configured() {
		return false, fmt.Errorf("forgejo not configured")
	}

	exists, err := c.userExists(username)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}

	pw, err := randomPassword()
	if err != nil {
		return false, err
	}
	body, _ := json.Marshal(map[string]any{
		"username":             username,
		"email":                email,
		"password":             pw,
		"must_change_password": true,
	})
	status, resp, err := c.do(http.MethodPost, "/admin/users", body)
	if err != nil {
		return false, err
	}
	if status < 200 || status >= 300 {
		return false, fmt.Errorf("forgejo create user %q: %d: %s", username, status, truncate(resp, 200))
	}
	return true, nil
}

// userExists reports whether a Forgejo user with this name is present.
func (c Config) userExists(username string) (bool, error) {
	status, resp, err := c.do(http.MethodGet, "/users/"+username, nil)
	if err != nil {
		return false, err
	}
	switch {
	case status == http.StatusOK:
		return true, nil
	case status == http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("forgejo lookup user %q: %d: %s", username, status, truncate(resp, 200))
	}
}

func (c Config) do(method, path string, body []byte) (int, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+"/api/v1"+path, reader)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Authorization", "token "+c.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(out), nil
}

func randomPassword() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
