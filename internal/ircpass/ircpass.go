// Package ircpass sets a member's chat/IRC password from the (non-root) BBS
// process. The Ergo password store (/var/lib/ergo/irc-passwd, ergo:ergo 0600)
// and each member's The Lounge user file are root-owned, so the BBS — which runs
// as an unprivileged service user — cannot write them directly. Instead it shells
// out to scripts/set-irc-password.sh through a narrow sudo rule (installed by
// setup.sh) that lets only that one command run as root for a single member.
//
// This is the chat leg of the unified "reset my password everywhere" flow
// (passwd@): git (Forgejo) and mail (Mailu) are set in-process via their admin
// APIs; chat is set here. The script writes the Ergo pbkdf2 hash AND syncs the
// member's The Lounge saslPassword, so native IRC clients and the web client both
// keep working with the same secret.
//
// Config (env):
//
//	AGENTBBS_SET_IRC_PASSWD   path to set-irc-password.sh (enables the chat leg)
//	AGENTBBS_SET_IRC_SUDO     "1" (default) to invoke it via sudo; "0" to call it
//	                          directly (e.g. when the BBS already runs as root, or
//	                          in tests). When sudo is used the binary is taken from
//	                          AGENTBBS_SUDO_BIN (default "sudo").
package ircpass

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Config locates the privileged helper and how to invoke it.
type Config struct {
	Script  string // path to set-irc-password.sh; empty disables the chat leg
	UseSudo bool   // run the script through sudo
	SudoBin string // sudo binary (default "sudo")
}

// ConfigFromEnv reads the chat-password settings from the environment.
func ConfigFromEnv() Config {
	return Config{
		Script:  strings.TrimSpace(os.Getenv("AGENTBBS_SET_IRC_PASSWD")),
		UseSudo: os.Getenv("AGENTBBS_SET_IRC_SUDO") != "0",
		SudoBin: env("AGENTBBS_SUDO_BIN", "sudo"),
	}
}

// Configured reports whether the chat password can actually be set (the helper
// script path is set). When false, callers skip the chat leg and say so.
func (c Config) Configured() bool { return c.Script != "" }

// SetPassword sets member's chat/IRC password by running the privileged helper as
// `set-irc-password.sh <member> -` (optionally via sudo), feeding the password on
// STDIN. Passing it on stdin — not argv — keeps it out of the process table (ps)
// and out of sudo's command log. member is the authenticated SSH account name; we
// still reject anything that isn't a plain account token as defence in depth, so
// it can never be read as a flag or path.
func (c Config) SetPassword(member, password string) error {
	if !c.Configured() {
		return fmt.Errorf("chat password helper not configured")
	}
	if !validMember(member) {
		return fmt.Errorf("invalid member name %q", member)
	}
	if password == "" || strings.ContainsAny(password, "\r\n") {
		return fmt.Errorf("invalid password")
	}

	// "-" tells the helper to read the password from stdin.
	name, args := c.Script, []string{member, "-"}
	if c.UseSudo {
		name = c.SudoBin
		args = []string{"-n", c.Script, member, "-"}
	}

	// Generous: the helper also resets The Lounge web-login password via a
	// `docker exec thelounge ...` which can take a few seconds on a busy box.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	// Never let the helper inherit the BBS environment wholesale; pass only the
	// store/Lounge paths it reads, so an operator override flows through.
	cmd.Env = passthroughEnv()
	cmd.Stdin = strings.NewReader(password + "\n")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("set-irc-password: %v: %s", err, strings.TrimSpace(out.String()))
	}
	return nil
}

// validMember accepts the same charset the BBS allows for account names
// ([a-z0-9-], the output of auth.SanitizeUsername) so a member string can never
// smuggle a flag or path separator into the helper's argv.
func validMember(m string) bool {
	if m == "" || len(m) > 32 || strings.HasPrefix(m, "-") {
		return false
	}
	for _, r := range m {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return true
}

// passthroughEnv builds a minimal environment for the helper: PATH plus the few
// AGENTBBS_/ERGO_ knobs that select the password store and Lounge user dir.
func passthroughEnv() []string {
	keep := []string{"PATH", "ERGO_IRC_PASSWD", "AGENTBBS_LOUNGE_USERS"}
	var env []string
	for _, k := range keep {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}
