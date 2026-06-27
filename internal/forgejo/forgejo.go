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

// LoginURL is the web sign-in page members are pointed at in their welcome
// email, e.g. https://git.profullstack.com/user/login.
func (c Config) LoginURL() string {
	return strings.TrimRight(c.BaseURL, "/") + "/user/login"
}

// EnsureUser creates a Forgejo account for username (forwarding to email) if it
// does not already exist. It is idempotent: created is false (and password "")
// when the account was already present. New accounts get a generated temporary
// password with must_change_password set; the caller emails it to the member so
// they can sign in to the web UI once and set their own. Git over SSH still uses
// their registered key.
func (c Config) EnsureUser(username, email string) (created bool, password string, err error) {
	if !c.Configured() {
		return false, "", fmt.Errorf("forgejo not configured")
	}

	exists, err := c.userExists(username)
	if err != nil {
		return false, "", err
	}
	if exists {
		return false, "", nil
	}

	pw, err := randomPassword()
	if err != nil {
		return false, "", err
	}
	body, _ := json.Marshal(map[string]any{
		"username":             username,
		"email":                email,
		"password":             pw,
		"must_change_password": true,
	})
	status, resp, err := c.do(http.MethodPost, "/admin/users", body)
	if err != nil {
		return false, "", err
	}
	if status < 200 || status >= 300 {
		return false, "", fmt.Errorf("forgejo create user %q: %d: %s", username, status, truncate(resp, 200))
	}
	return true, pw, nil
}

// EnsureUserReset creates the account if missing, or resets an existing
// account's password to a fresh temporary one with must_change_password set.
// Unlike EnsureUser it always returns a usable password — even for accounts
// that already exist (whose original one-time password we no longer hold).
// created reports whether the account was newly made. Used by the notify-creds
// re-send so every member receives working web credentials.
func (c Config) EnsureUserReset(username, email string) (created bool, password string, err error) {
	if !c.Configured() {
		return false, "", fmt.Errorf("forgejo not configured")
	}
	exists, err := c.userExists(username)
	if err != nil {
		return false, "", err
	}
	pw, err := randomPassword()
	if err != nil {
		return false, "", err
	}
	if !exists {
		body, _ := json.Marshal(map[string]any{
			"username":             username,
			"email":                email,
			"password":             pw,
			"must_change_password": true,
		})
		status, resp, err := c.do(http.MethodPost, "/admin/users", body)
		if err != nil {
			return false, "", err
		}
		if status < 200 || status >= 300 {
			return false, "", fmt.Errorf("forgejo create user %q: %d: %s", username, status, truncate(resp, 200))
		}
		return true, pw, nil
	}
	// Reset the existing account's password. login_name/source_id are optional
	// for local accounts in current Forgejo, so we send only the fields we change.
	body, _ := json.Marshal(map[string]any{
		"password":             pw,
		"must_change_password": true,
	})
	status, resp, err := c.do(http.MethodPatch, "/admin/users/"+username, body)
	if err != nil {
		return false, "", err
	}
	if status < 200 || status >= 300 {
		return false, "", fmt.Errorf("forgejo reset user %q: %d: %s", username, status, truncate(resp, 200))
	}
	return false, pw, nil
}

// SetPassword sets an existing account's password to the member-chosen value and
// clears must_change_password (they picked it, so don't force another change on
// next sign-in). Unlike EnsureUserReset it never generates a password and never
// creates the account: the caller is a member resetting their own credential
// across services, and the Forgejo account is expected to already exist (it is
// created at email-verification time). A missing account is reported as an error
// so the caller can surface "no git account yet" rather than silently succeeding.
func (c Config) SetPassword(username, password string) error {
	if !c.Configured() {
		return fmt.Errorf("forgejo not configured")
	}
	exists, err := c.userExists(username)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("forgejo user %q does not exist", username)
	}
	body, _ := json.Marshal(map[string]any{
		"password":             password,
		"must_change_password": false,
	})
	status, resp, err := c.do(http.MethodPatch, "/admin/users/"+username, body)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("forgejo set password %q: %d: %s", username, status, truncate(resp, 200))
	}
	return nil
}

// EnsureKey registers an SSH public key on the member's Forgejo account so the
// key they use for the BBS is also their git push key ("BBS membership is the
// git account"). It is idempotent: added is false when the same key material is
// already present. A blank key is a no-op. title labels the key in Forgejo.
func (c Config) EnsureKey(username, title, pubKey string) (added bool, err error) {
	if !c.Configured() {
		return false, fmt.Errorf("forgejo not configured")
	}
	pubKey = strings.TrimSpace(pubKey)
	if pubKey == "" {
		return false, nil
	}

	// Skip if this key (ignoring the trailing comment) is already on the account.
	if status, resp, e := c.do(http.MethodGet, "/users/"+username+"/keys", nil); e == nil && status == http.StatusOK {
		var keys []struct {
			Key string `json:"key"`
		}
		if json.Unmarshal([]byte(resp), &keys) == nil {
			want := keyMaterial(pubKey)
			for _, k := range keys {
				if keyMaterial(k.Key) == want {
					return false, nil
				}
			}
		}
	}

	body, _ := json.Marshal(map[string]any{"title": title, "key": pubKey, "read_only": false})
	status, resp, err := c.do(http.MethodPost, "/admin/users/"+username+"/keys", body)
	if err != nil {
		return false, err
	}
	if status == http.StatusUnprocessableEntity {
		return false, nil // key already exists (raced or comment differs)
	}
	if status < 200 || status >= 300 {
		return false, fmt.Errorf("forgejo add key %q: %d: %s", username, status, truncate(resp, 200))
	}
	return true, nil
}

// keyMaterial returns the type+base64 of an authorized-key line, dropping the
// optional comment so the same key compares equal regardless of how it's labeled.
func keyMaterial(authorizedKey string) string {
	f := strings.Fields(strings.TrimSpace(authorizedKey))
	if len(f) >= 2 {
		return f[0] + " " + f[1]
	}
	return strings.TrimSpace(authorizedKey)
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
