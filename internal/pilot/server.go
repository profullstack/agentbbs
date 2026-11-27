package pilot

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	gossh "golang.org/x/crypto/ssh"
)

const (
	maxPilotRequestBytes  = 64 * 1024
	maxPilotResponseBytes = 256 * 1024
	maxPilotAge           = 24 * time.Hour
	maxPilotTTL           = 7 * 24 * time.Hour
	maxPilotFutureSkew    = time.Hour
)

// Config controls the deliberately narrow pilot server.
type Config struct {
	Address           string
	HostKeyPath       string
	RegistryPath      string
	DatabasePath      string
	MaxRequestBytes   int
	MaxResponseBytes  int
	MaxStatuses       int
	MaxAge            time.Duration
	MaxTTL            time.Duration
	MaxFutureSkew     time.Duration
	RateWindow        time.Duration
	RequestsPerWindow int
}

// ConfigFromEnv keeps pilot activation explicit and safe by default. The
// default address is loopback; a non-loopback address must be a literal
// 100.64.0.0/10 tailnet address, and peer connections are checked against the
// same range. Live firewall evidence is still required before deployment.
func ConfigFromEnv(dataDir string) Config {
	return Config{
		Address:           env("AGENTBBS_PILOT_ADDR", "127.0.0.1:2223"),
		HostKeyPath:       env("AGENTBBS_PILOT_HOST_KEY", filepath.Join(dataDir, "pilot", "host_ed25519")),
		RegistryPath:      env("AGENTBBS_PILOT_KEYS", filepath.Join(dataDir, "pilot", "keys.json")),
		DatabasePath:      env("AGENTBBS_PILOT_DB", filepath.Join(dataDir, "pilot", "status.db")),
		MaxRequestBytes:   envInt("AGENTBBS_PILOT_MAX_REQUEST_BYTES", defaultMaxRequestBytes),
		MaxResponseBytes:  envInt("AGENTBBS_PILOT_MAX_RESPONSE_BYTES", defaultMaxResponseBytes),
		MaxStatuses:       envInt("AGENTBBS_PILOT_MAX_STATUSES", defaultMaxStatuses),
		MaxAge:            envDuration("AGENTBBS_PILOT_MAX_AGE", 15*time.Minute),
		MaxTTL:            envDuration("AGENTBBS_PILOT_MAX_TTL", time.Hour),
		MaxFutureSkew:     envDuration("AGENTBBS_PILOT_MAX_FUTURE_SKEW", 5*time.Minute),
		RateWindow:        envDuration("AGENTBBS_PILOT_RATE_WINDOW", time.Minute),
		RequestsPerWindow: envInt("AGENTBBS_PILOT_REQUESTS_PER_WINDOW", 30),
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if value := os.Getenv(key); value != "" {
		var n int
		if _, err := fmt.Sscan(value, &n); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if d, err := time.ParseDuration(value); err == nil && d > 0 {
			return d
		}
	}
	return fallback
}

func validateConfig(cfg Config) error {
	if cfg.MaxRequestBytes <= 0 || cfg.MaxRequestBytes > maxPilotRequestBytes || cfg.MaxResponseBytes < 64 || cfg.MaxResponseBytes > maxPilotResponseBytes || cfg.MaxStatuses <= 0 || cfg.MaxStatuses > defaultMaxStatuses {
		return errors.New("pilot size limits are invalid")
	}
	if cfg.MaxAge <= 0 || cfg.MaxAge > maxPilotAge || cfg.MaxTTL <= 0 || cfg.MaxTTL > maxPilotTTL || cfg.MaxFutureSkew < 0 || cfg.MaxFutureSkew > maxPilotFutureSkew || cfg.RateWindow <= 0 || cfg.RequestsPerWindow <= 0 {
		return errors.New("pilot time or rate limits are invalid")
	}
	host, _, err := net.SplitHostPort(cfg.Address)
	if err != nil {
		return fmt.Errorf("pilot address must be host:port: %w", err)
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	if ip == nil {
		return errors.New("pilot address must use a literal loopback or 100.64.0.0/10 tailnet IP")
	}
	if !ip.IsLoopback() && !inTailnetRange(ip) {
		return errors.New("pilot address must be loopback or tailnet CGNAT (100.64.0.0/10)")
	}
	return nil
}

func inTailnetRange(ip net.IP) bool {
	_, network, _ := net.ParseCIDR("100.64.0.0/10")
	return ip != nil && network.Contains(ip)
}

func allowedPeer(bindAddress string, remote net.Addr) bool {
	if remote == nil {
		return false
	}
	host, _, err := net.SplitHostPort(remote.String())
	if err != nil {
		return false
	}
	peer := net.ParseIP(strings.Trim(host, "[]"))
	if peer == nil {
		return false
	}
	bindHost, _, err := net.SplitHostPort(bindAddress)
	if err != nil {
		return false
	}
	bind := net.ParseIP(strings.Trim(bindHost, "[]"))
	if bind == nil {
		return false
	}
	if bind.IsLoopback() {
		return peer.IsLoopback() || inTailnetRange(peer)
	}
	return inTailnetRange(peer)
}

type rateLimiter struct {
	mu     sync.Mutex
	seen   map[string][]time.Time
	window time.Duration
	max    int
}

func newRateLimiter(window time.Duration, max int) *rateLimiter {
	return &rateLimiter{seen: map[string][]time.Time{}, window: window, max: max}
}

func (l *rateLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-l.window)
	items := l.seen[key]
	kept := items[:0]
	for _, at := range items {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	if len(kept) >= l.max {
		l.seen[key] = kept
		return false
	}
	l.seen[key] = append(kept, now)
	return true
}

// Run starts the pilot SSH service and returns when ctx is canceled or the
// server fails. No normal AgentBBS listeners, plugins, web routes, or join
// flows are initialized by this path.
func Run(ctx context.Context, cfg Config) error {
	if err := validateConfig(cfg); err != nil {
		return err
	}
	registry, err := LoadRegistry(cfg.RegistryPath)
	if err != nil {
		return err
	}
	st, err := Open(cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := ensurePrivateDir(filepath.Dir(cfg.HostKeyPath)); err != nil {
		return fmt.Errorf("pilot host-key directory: %w", err)
	}
	if _, err := os.Lstat(cfg.HostKeyPath); err == nil {
		if err := validatePrivateFile(cfg.HostKeyPath); err != nil {
			return fmt.Errorf("pilot host key: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect pilot host key: %w", err)
	}
	limiter := newRateLimiter(cfg.RateWindow, cfg.RequestsPerWindow)
	server, err := wish.NewServer(
		wish.WithAddress(cfg.Address),
		ssh.HostKeyFile(cfg.HostKeyPath),
		ssh.NoPty(),
		wish.WithPublicKeyAuth(func(ctx ssh.Context, key ssh.PublicKey) bool {
			_, ok := registry.Authorize(ctx.User(), fingerprint(key))
			return ok
		}),
		wish.WithPasswordAuth(func(ssh.Context, string) bool { return false }),
		wish.WithKeyboardInteractiveAuth(func(ssh.Context, gossh.KeyboardInteractiveChallenge) bool { return false }),
		wish.WithIdleTimeout(30*time.Second),
		wish.WithMiddleware(pilotMiddleware(registry, st, cfg, limiter)),
	)
	if err != nil {
		return fmt.Errorf("create pilot SSH server: %w", err)
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	err = server.ListenAndServe()
	if errors.Is(err, ssh.ErrServerClosed) && ctx.Err() != nil {
		return nil
	}
	return err
}

func fingerprint(key ssh.PublicKey) string {
	if key == nil {
		return ""
	}
	return gossh.FingerprintSHA256(key)
}

func pilotMiddleware(registry Registry, st *Store, cfg Config, limiter *rateLimiter) wish.Middleware {
	return func(next ssh.Handler) ssh.Handler {
		return func(s ssh.Session) {
			if !allowedPeer(cfg.Address, s.RemoteAddr()) {
				_ = s.Exit(1)
				return
			}
			fp := fingerprint(s.PublicKey())
			entry, ok := registry.Authorize(s.User(), fp)
			if !ok || !limiter.allow(fp, time.Now()) {
				_ = s.Exit(1)
				return
			}
			args := s.Command()
			if len(args) != 1 || (args[0] != "publish" && args[0] != "query") {
				writeError(s, "command must be exactly publish or query", cfg.MaxResponseBytes)
				return
			}
			switch args[0] {
			case "publish":
				handlePublish(s, entry, st, cfg)
			case "query":
				handleQuery(s, entry, st, cfg)
			}
		}
	}
}

func handlePublish(s ssh.Session, identity KeyEntry, st *Store, cfg Config) {
	var req publishRequest
	if err := readJSONRequest(s, cfg.MaxRequestBytes, &req); err != nil {
		writeError(s, err.Error(), cfg.MaxResponseBytes)
		return
	}
	now := time.Now().UTC()
	if !containsAudience(identity.PublishAudiences, req.Audience) {
		writeError(s, "publish audience is not authorized", cfg.MaxResponseBytes)
		return
	}
	status, err := parseAndValidatePublish(req, identity, now, cfg)
	if err != nil {
		writeError(s, err.Error(), cfg.MaxResponseBytes)
		return
	}
	if err := st.Publish(status); err != nil {
		writeError(s, err.Error(), cfg.MaxResponseBytes)
		return
	}
	writeJSON(s, map[string]any{"ok": true, "subject": status.Subject, "audience": status.Audience, "revision": status.Revision}, cfg.MaxResponseBytes)
}

func handleQuery(s ssh.Session, identity KeyEntry, st *Store, cfg Config) {
	var req queryRequest
	if err := readJSONRequest(s, cfg.MaxRequestBytes, &req); err != nil {
		writeError(s, err.Error(), cfg.MaxResponseBytes)
		return
	}
	if !containsAudience(identity.QueryAudiences, req.Audience) {
		writeError(s, "query audience is not authorized", cfg.MaxResponseBytes)
		return
	}
	if err := validateQuery(req, cfg); err != nil {
		writeError(s, err.Error(), cfg.MaxResponseBytes)
		return
	}
	limit := req.Limit
	if limit == 0 {
		limit = min(cfg.MaxStatuses, defaultMaxStatuses)
	}
	rows, err := st.Query(req.Audience, req.Subject, time.Now().UTC(), limit)
	if err != nil {
		writeError(s, err.Error(), cfg.MaxResponseBytes)
		return
	}
	writeJSON(s, map[string]any{"ok": true, "audience": req.Audience, "statuses": rows}, cfg.MaxResponseBytes)
}

func writeError(s ssh.Session, message string, maxBytes int) {
	writeJSONWithExit(s, map[string]any{"ok": false, "error": message}, maxBytes, 1)
}

func writeJSON(s ssh.Session, value any, maxBytes int) {
	writeJSONWithExit(s, value, maxBytes, 0)
}

func writeJSONWithExit(s ssh.Session, value any, maxBytes, exitCode int) {
	b, err := responseWithinLimit(value, maxBytes)
	if err != nil {
		b = []byte("{\"ok\":false,\"error\":\"response exceeds size limit\"}\n")
		exitCode = 1
	}
	_, _ = s.Write(b)
	_ = s.Exit(exitCode)
}

var _ = gossh.FingerprintSHA256
