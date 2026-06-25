// Package motd fetches the shared Message of the Day from profullstack.com and
// caches it in memory, refreshing in the background. The hub MOTD reads the
// cached value (never blocking session start); a failed fetch keeps the last
// known value. Set AGENTBBS_MOTD_URL to override the source (empty disables).
package motd

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// DefaultURL is the canonical shared MOTD endpoint (plain text, CORS-open).
const DefaultURL = "https://profullstack.com/motd"

var (
	mu     sync.RWMutex
	cached string
)

// Current returns the most recently fetched MOTD, or "" if none has been
// fetched yet (or fetching is disabled/failing).
func Current() string {
	mu.RLock()
	defer mu.RUnlock()
	return cached
}

func set(s string) {
	mu.Lock()
	cached = s
	mu.Unlock()
}

func fetch(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", &httpError{resp.StatusCode}
	}
	// Cap the read; the MOTD is a short text blob.
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

type httpError struct{ code int }

func (e *httpError) Error() string { return "motd: unexpected status " + http.StatusText(e.code) }

// Start fetches the MOTD once (blocking, with a short timeout so a slow source
// can't stall boot for long) then refreshes every `every` until ctx is done.
// A url of "" disables fetching entirely.
func Start(ctx context.Context, url string, every time.Duration) {
	if url == "" {
		return
	}
	refresh := func() {
		c, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		if s, err := fetch(c, url); err == nil && s != "" {
			set(s)
		}
	}
	refresh()
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				refresh()
			}
		}
	}()
}
