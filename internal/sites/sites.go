// Package sites maps members' custom domains onto their homepage (the
// public_html that is also served at /~name).
//
// How it works without a custom Caddy module:
//
//   - The DB (store.domains) is the source of truth for domain→user.
//   - A symlink farm at <data>/domains/<domain> -> <data>/users/<name>/public_html
//     lets Caddy serve any mapped domain with `root * <data>/domains/{host}`.
//   - AskHandler answers Caddy's on-demand-TLS query so certificates are only
//     issued for domains that are actually mapped (no open cert relay).
//
// A member points DNS (CNAME or A) at the BBS host; the moment a TLS handshake
// for that domain arrives, Caddy asks us, gets a 200, provisions a cert, and
// serves their homepage. See docs/custom-domains.md.
package sites

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/profullstack/agentbbs/internal/store"
)

// ErrInvalidDomain is returned for syntactically invalid hostnames.
var ErrInvalidDomain = errors.New("invalid domain")

// A conservative DNS hostname: lowercase labels, a real TLD, no scheme/path.
var domainRe = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$`)

// Normalize lowercases and trims a domain (drops a trailing dot, any scheme).
func Normalize(d string) string {
	d = strings.ToLower(strings.TrimSpace(d))
	d = strings.TrimPrefix(d, "https://")
	d = strings.TrimPrefix(d, "http://")
	d = strings.TrimSuffix(d, "/")
	return strings.TrimSuffix(d, ".")
}

// Valid reports whether d is a usable custom domain.
func Valid(d string) bool { return len(d) <= 253 && domainRe.MatchString(d) }

// Manager owns the symlink farm and the on-demand-TLS ask endpoint.
type Manager struct {
	st       store.Store
	usersDir string // <data>/users
	domDir   string // <data>/domains  (the symlink farm Caddy serves)
}

// NewManager prepares the symlink farm under dataDir/domains.
func NewManager(st store.Store, dataDir string) (*Manager, error) {
	domDir := filepath.Join(dataDir, "domains")
	if err := os.MkdirAll(domDir, 0o755); err != nil {
		return nil, err
	}
	return &Manager{
		st:       st,
		usersDir: filepath.Join(dataDir, "users"),
		domDir:   domDir,
	}, nil
}

// Add maps domain→username (DB row + symlink). Returns store.ErrDomainTaken if
// another member already owns it, or ErrInvalidDomain on a malformed host.
func (m *Manager) Add(domain, username string) (string, error) {
	domain = Normalize(domain)
	if !Valid(domain) {
		return "", ErrInvalidDomain
	}
	if err := m.st.MapDomain(domain, username); err != nil {
		return domain, err
	}
	return domain, m.link(domain, username)
}

// Remove unmaps a domain owned by username (DB row + symlink).
func (m *Manager) Remove(domain, username string) (string, error) {
	domain = Normalize(domain)
	if err := m.st.UnmapDomain(domain, username); err != nil {
		return domain, err
	}
	_ = os.Remove(filepath.Join(m.domDir, domain))
	return domain, nil
}

// List returns the domains mapped to username.
func (m *Manager) List(username string) ([]string, error) {
	return m.st.DomainsForUser(username)
}

// Sync rebuilds the symlink farm from the DB. Run at startup so the farm
// survives a wiped data dir or hand edits, and so the DB stays authoritative.
func (m *Manager) Sync() error {
	all, err := m.st.AllDomains()
	if err != nil {
		return err
	}
	for _, dm := range all {
		if err := m.link(dm.Domain, dm.Username); err != nil {
			return err
		}
	}
	return nil
}

// link points <domDir>/<domain> at the user's public_html, replacing any stale
// link. The public_html is created if absent so the cert + serve path is ready
// before the member has logged in to seed a homepage.
func (m *Manager) link(domain, username string) error {
	if !Valid(domain) {
		return ErrInvalidDomain
	}
	target := filepath.Join(m.usersDir, username, "public_html")
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	link := filepath.Join(m.domDir, domain)
	_ = os.Remove(link)
	return os.Symlink(target, link)
}

// AskHandler answers Caddy's on-demand-TLS query: 200 when the domain is
// mapped (so a cert may be issued), 404 otherwise. Caddy passes the requested
// host as ?domain=. Bind this to loopback only.
func (m *Manager) AskHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d := Normalize(r.URL.Query().Get("domain"))
		if d == "" || !Valid(d) {
			http.Error(w, "bad domain", http.StatusBadRequest)
			return
		}
		if _, ok, err := m.st.DomainUser(d); err != nil {
			http.Error(w, "lookup error", http.StatusInternalServerError)
			return
		} else if !ok {
			http.Error(w, "unknown domain", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

// ServeAsk runs the ask endpoint (blocking). Intended for a goroutine.
func (m *Manager) ServeAsk(addr string) error {
	return http.ListenAndServe(addr, m.AskHandler())
}
