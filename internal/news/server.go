package news

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/profullstack/agentbbs/internal/news/nntpd"
)

// DefaultAddr is the loopback plaintext listener the in-BBS `news@` reader dials.
// The public surface is the TLS (NNTPS) listener; see ServeTLS.
const DefaultAddr = "127.0.0.1:1119"

// DefaultGroups are seeded on first boot if AGENTBBS_NEWS_GROUPS is unset.
var DefaultGroups = []GroupSpec{
	{"pfs.announce", "Official announcements (read-mostly)"},
	{"pfs.general", "General discussion for members"},
	{"pfs.agents", "For and about AI agents on the BBS"},
	{"pfs.support", "Help, questions, and bug reports"},
}

// GroupSpec is a newsgroup to seed.
type GroupSpec struct{ Name, Description string }

// Server hosts the members-only NNTP network over the shared store.
type Server struct {
	st   NewsStore
	srv  *nntpd.Server
	host string
}

// New builds a news Server. host is the public hostname used in Message-IDs and
// stamped From addresses (e.g. "news.profullstack.com").
func New(st NewsStore, host string) *Server {
	return &Server{
		st:   st,
		srv:  nntpd.NewServer(&backend{st: st, host: host}),
		host: host,
	}
}

// SeedGroups ensures the given groups exist. A nil/empty slice seeds
// DefaultGroups. Idempotent.
func (s *Server) SeedGroups(groups []GroupSpec) error {
	if len(groups) == 0 {
		groups = DefaultGroups
	}
	for _, g := range groups {
		if err := s.st.EnsureNewsGroup(g.Name, g.Description); err != nil {
			return err
		}
	}
	return nil
}

// ParseGroups turns the AGENTBBS_NEWS_GROUPS env value into GroupSpecs. Each
// entry is "name" or "name:description", comma- or whitespace-separated.
func ParseGroups(s string) []GroupSpec {
	var out []GroupSpec
	for _, field := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' }) {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		name, desc, _ := strings.Cut(field, ":")
		out = append(out, GroupSpec{Name: strings.TrimSpace(name), Description: strings.TrimSpace(desc)})
	}
	return out
}

// Serve accepts connections on ln until ctx is cancelled. The anonymous backend
// is immutable and AUTHINFO swaps only the per-session backend, so one Server is
// safe to share across all connections.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	go func() { <-ctx.Done(); _ = ln.Close() }()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.srv.Process(conn)
	}
}

// ServeLoopback listens for plaintext NNTP on addr (loopback only — it carries
// no TLS, so it must never be a public interface).
func (s *Server) ServeLoopback(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return s.Serve(ctx, ln)
}

// ServeTLS listens for NNTPS on addr using the certificate at certFile/keyFile,
// reloading it from disk when it changes (so Caddy cert renewals are picked up
// without a restart).
func (s *Server) ServeTLS(ctx context.Context, addr, certFile, keyFile string) error {
	k := &certKeeper{certFile: certFile, keyFile: keyFile}
	if _, err := k.get(); err != nil {
		return fmt.Errorf("load news TLS cert: %w", err)
	}
	ln, err := tls.Listen("tcp", addr, &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return k.get() },
	})
	if err != nil {
		return err
	}
	return s.Serve(ctx, ln)
}

// certKeeper loads a TLS keypair from disk and reloads it when either file's
// modification time changes.
type certKeeper struct {
	certFile, keyFile string

	mu      sync.Mutex
	cert    *tls.Certificate
	modSum  int64
	checked time.Time
}

func (k *certKeeper) get() (*tls.Certificate, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	// Re-stat at most every 30s to keep the handshake hot path cheap.
	if k.cert != nil && time.Since(k.checked) < 30*time.Second {
		return k.cert, nil
	}
	k.checked = time.Now()

	sum := statSum(k.certFile) ^ statSum(k.keyFile)
	if k.cert != nil && sum == k.modSum {
		return k.cert, nil
	}
	cert, err := tls.LoadX509KeyPair(k.certFile, k.keyFile)
	if err != nil {
		if k.cert != nil {
			return k.cert, nil // keep serving the last good cert on a transient read error
		}
		return nil, err
	}
	k.cert, k.modSum = &cert, sum
	return k.cert, nil
}

func statSum(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.ModTime().UnixNano() ^ fi.Size()
}
