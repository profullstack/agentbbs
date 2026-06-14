// Package tor fetches URLs and wraps commands so they egress through the host's
// Tor SOCKS proxy. tor-url fetches run on the host (constrained); generic and
// IRC commands run inside the member's pod via torsocks (see cmd/agentbbs).
package tor

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

// SocksAddr is the host-local Tor SOCKS5 endpoint that setup.sh's tor service
// listens on. Overridable so a dev host can point elsewhere.
var SocksAddr = envOr("AGENTBBS_TOR_SOCKS", "127.0.0.1:9050")

// Fetch limits so a single member can't tie up the host.
const (
	fetchTimeout = 30 * time.Second
	maxBytes     = 2_000_000 // 2 MB
)

// FetchURL retrieves rawURL over Tor and returns the body (capped at maxBytes).
// Only http/https are allowed and the call is bounded by a timeout, so the URL
// (passed to curl as a single argv element — never a shell) can't be abused to
// reach the local network or run unboundedly.
func FetchURL(ctx context.Context, rawURL string) ([]byte, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("give an http(s) URL, e.g. http://example.onion")
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "curl",
		"-sS", "-L", "--max-redirs", "3",
		"--max-time", fmt.Sprint(int(fetchTimeout.Seconds())),
		"--max-filesize", fmt.Sprint(maxBytes),
		"--proto", "=http,https",
		"--socks5-hostname", SocksAddr, // resolve the hostname through Tor too
		u.String(),
	)
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("timed out after %s", fetchTimeout)
		}
		return nil, fmt.Errorf("fetch failed (is the host reachable over Tor?): %v", err)
	}
	if len(out) > maxBytes {
		out = out[:maxBytes]
	}
	return out, nil
}

// Torsocks prefixes argv with torsocks so the command's network traffic is
// routed through Tor when run inside a pod that has torsocks configured.
func Torsocks(argv []string) []string {
	return append([]string{"torsocks"}, argv...)
}

// IRCArgv builds the argv for an interactive IRC-over-Tor session to server
// (host[:port]); it runs irssi through torsocks. server is validated by the
// caller. Defaults to the standard IRC port when none is given.
func IRCArgv(server string) []string {
	host, port := server, "6667"
	if i := strings.LastIndex(server, ":"); i > 0 {
		host, port = server[:i], server[i+1:]
	}
	return Torsocks([]string{"irssi", "--connect=" + host, "--port=" + port})
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}
