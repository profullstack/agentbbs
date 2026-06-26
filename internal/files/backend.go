// Package files implements member file storage for AgentBBS, reachable over
// SFTP with the member's existing SSH login key (docs/files.md). It is wired as
// an "sftp" subsystem on the shared wish server (port 22), so a member runs
//
//	sftp files@bbs.profullstack.com
//
// and lands in a virtual filesystem with two areas:
//
//	/me      — their private, per-user workspace (quota-limited)
//	/public  — a single shared file area (old-school BBS file area); world-read,
//	           members-only write by default, operator-moderated.
//
// Identity is the SSH public key (one key = one account, like the rest of the
// BBS); the SFTP username is conventional ("files") and ignored. The server is a
// fully virtual Go SFTP server (github.com/pkg/sftp) — there are no OS users.
//
// Security: every path the client supplies is resolved through resolve() in
// fs.go, which confines it to its area root (no traversal, no symlink escape);
// see the path-traversal tests. Per §9.2 the operator can inspect and act on
// hosted files (the management TUI), and per §9.3 (amended) the only sharing
// surface is the single public area — there is no workspace-to-workspace
// transfer.
package files

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/profullstack/agentbbs/internal/store"
)

// Setting keys persisted in files_settings.
const (
	// settingPublicWrite is "members" (default) or "off". When "off", the
	// shared public area is read-only for everyone (operators moderate via the
	// management TUI / filesystem).
	settingPublicWrite = "public_write"
)

// DefaultQuota is the per-user workspace quota when none is configured.
const DefaultQuota int64 = 1 << 30 // 1 GiB

// FilesStore is the slice of the store the Files service needs.
type FilesStore interface {
	UserByFingerprint(fp string) (store.User, bool, error)
	UserByName(name string) (store.User, bool, error)
	ListUsers(limit int) ([]store.User, error)
	FilesAccess(userID int64) (store.FilesAccess, error)
	SetFilesQuota(userID, bytes int64) error
	SetFilesRevoked(userID int64, revoked bool) error
	FilesSetting(key string) (string, bool, error)
	SetFilesSetting(key, value string) error
}

// Config configures the Files service.
type Config struct {
	// Root is the storage root, e.g. <dataDir>/files. The service owns
	// <Root>/users/<name> (private) and <Root>/public (shared).
	Root string
	// DefaultQuota is the per-user workspace quota in bytes (0 → DefaultQuota).
	DefaultQuota int64
}

// Service is the shared Files engine: one instance backs the SFTP subsystem,
// the in-BBS browser, and the operator management TUI.
type Service struct {
	st  FilesStore
	cfg Config
	reg *registry
}

// New builds a Files service and ensures the storage layout exists.
func New(st FilesStore, cfg Config) (*Service, error) {
	if cfg.DefaultQuota <= 0 {
		cfg.DefaultQuota = DefaultQuota
	}
	cfg.Root = filepath.Clean(cfg.Root)
	for _, d := range []string{cfg.Root, filepath.Join(cfg.Root, "users"), filepath.Join(cfg.Root, "sites"), filepath.Join(cfg.Root, "public")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	return &Service{st: st, cfg: cfg, reg: newRegistry()}, nil
}

// privRoot is the absolute private-workspace directory for a member.
func (s *Service) privRoot(user string) string {
	return filepath.Join(s.cfg.Root, "users", user)
}

// pubRoot is the absolute shared public-area directory.
func (s *Service) pubRoot() string { return filepath.Join(s.cfg.Root, "public") }

// siteRoot is the absolute per-user public ("site") directory for a member,
// served unauthenticated at ~<name> on the web file host.
func (s *Service) siteRoot(user string) string {
	return filepath.Join(s.cfg.Root, "sites", user)
}

// ensureWorkspace creates a member's private workspace if absent.
func (s *Service) ensureWorkspace(user string) error {
	return os.MkdirAll(s.privRoot(user), 0o700)
}

// ensureSite creates a member's public site directory if absent. It is
// world-readable (0o755) because the web host serves it anonymously at ~<name>.
func (s *Service) ensureSite(user string) error {
	return os.MkdirAll(s.siteRoot(user), 0o755)
}

// ownedUsage sums the member-owned areas — their private /me workspace plus
// their public /site — for the quota gauge. The shared /public area is
// operator-managed and is not metered per user.
func (s *Service) ownedUsage(user string) (int64, error) {
	priv, err := dirSize(s.privRoot(user))
	if err != nil {
		return 0, err
	}
	site, err := dirSize(s.siteRoot(user))
	if err != nil {
		return 0, err
	}
	return priv + site, nil
}

// quotaFor returns the effective quota (bytes) for a user: their per-user
// override if set, else the server default.
func (s *Service) quotaFor(userID int64) int64 {
	if fa, err := s.st.FilesAccess(userID); err == nil && fa.QuotaBytes > 0 {
		return fa.QuotaBytes
	}
	return s.cfg.DefaultQuota
}

// publicWritable reports whether members may write to the shared area. The
// persisted files_settings value wins; default is true (members-only write).
func (s *Service) publicWritable() bool {
	if v, ok, err := s.st.FilesSetting(settingPublicWrite); err == nil && ok {
		return v != "off"
	}
	return true
}

// SetPublicWrite toggles members' write access to the shared public area.
func (s *Service) SetPublicWrite(on bool) error {
	v := "off"
	if on {
		v = "members"
	}
	return s.st.SetFilesSetting(settingPublicWrite, v)
}

// dirSize returns the total bytes used under root (regular files only; symlinks
// are not followed). A missing root counts as 0.
func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			total += info.Size()
		}
		return nil
	})
	if os.IsNotExist(err) {
		return 0, nil
	}
	return total, err
}

// SitePeer is a member with a published public site, for the anonymous ~user
// directory index on the web file host.
type SitePeer struct {
	Name  string
	Bytes int64
}

// PublicSites lists members who have published anything to their public /site,
// sorted by name — the source for the anonymous ~user directory at the root of
// the web file host. Members with an empty site are omitted.
func (s *Service) PublicSites() ([]SitePeer, error) {
	users, err := s.st.ListUsers(10000)
	if err != nil {
		return nil, err
	}
	out := make([]SitePeer, 0, len(users))
	for _, u := range users {
		if u.Banned {
			continue
		}
		n, err := dirSize(s.siteRoot(u.Name))
		if err != nil || n == 0 {
			continue
		}
		out = append(out, SitePeer{Name: u.Name, Bytes: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// AnonRoot resolves the on-disk root for an anonymous, read-only browse target:
// the shared public area (name == "") or a member's public site (name = the
// ~handle). ok is false when the named member does not exist or is banned. The
// returned root is a confinement boundary — callers must safeJoin onto it.
func (s *Service) AnonRoot(name string) (root string, ok bool, err error) {
	if name == "" {
		return s.pubRoot(), true, nil
	}
	u, found, err := s.st.UserByName(name)
	if err != nil {
		return "", false, err
	}
	if !found || u.Banned {
		return "", false, nil
	}
	// Materialize the (idempotent) site dir so ~name is browsable the moment the
	// account exists — before the member's first SFTP/web session creates it.
	// Without this, joining onto a missing root trips the escape guard.
	if err := s.ensureSite(u.Name); err != nil {
		return "", false, err
	}
	return s.siteRoot(u.Name), true, nil
}

// SafeJoin exposes the area-confinement join (lexical + symlink-escape guard)
// for the web host's anonymous read-only surface.
func (s *Service) SafeJoin(root, rel string) (string, error) { return safeJoin(root, rel) }

// Usage is a member's workspace usage snapshot.
type Usage struct {
	Bytes int64
	Quota int64
}

// Free reports remaining bytes (never negative).
func (u Usage) Free() int64 {
	if u.Quota <= u.Bytes {
		return 0
	}
	return u.Quota - u.Bytes
}

// Usage computes a member's owned-storage usage (private /me + public /site)
// against their quota.
func (s *Service) Usage(u store.User) (Usage, error) {
	used, err := s.ownedUsage(u.Name)
	if err != nil {
		return Usage{}, err
	}
	return Usage{Bytes: used, Quota: s.quotaFor(u.ID)}, nil
}

// --- live session registry (for the management TUI's Sessions pane) ----------

// Conn is a snapshot of a live SFTP connection, as shown by the management TUI.
type Conn struct {
	ID      int64
	User    string
	Key     string // SSH key fingerprint
	Remote  string
	Started time.Time
	RX      int64 // bytes received from the client (uploads)
	TX      int64 // bytes sent to the client (downloads)
}

// liveConn is the registry's mutable view of an active connection. Its atomic
// counters are updated by countingRWC; snapshots copy out plain Conn values.
type liveConn struct {
	id      int64
	user    string
	key     string
	remote  string
	started time.Time
	rxBytes atomic.Int64
	txBytes atomic.Int64
	closer  func() error
}

type registry struct {
	mu   sync.Mutex
	next int64
	live map[int64]*liveConn
}

func newRegistry() *registry { return &registry{live: map[int64]*liveConn{}} }

func (r *registry) add(user, key, remote string, closer func() error) *liveConn {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	c := &liveConn{id: r.next, user: user, key: key, remote: remote, started: time.Now(), closer: closer}
	r.live[c.id] = c
	return c
}

func (r *registry) remove(id int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.live, id)
}

func (r *registry) snapshot() []Conn {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Conn, 0, len(r.live))
	for _, c := range r.live {
		out = append(out, Conn{
			ID: c.id, User: c.user, Key: c.key, Remote: c.remote, Started: c.started,
			RX: c.rxBytes.Load(), TX: c.txBytes.Load(),
		})
	}
	return out
}

// Sessions returns a snapshot of live SFTP connections, newest first.
func (s *Service) Sessions() []Conn {
	cs := s.reg.snapshot()
	for i, j := 0, len(cs)-1; i < j; i, j = i+1, j-1 {
		cs[i], cs[j] = cs[j], cs[i]
	}
	return cs
}

// --- operator / management surface ------------------------------------------

// Users lists accounts for the management TUI (newest first).
func (s *Service) Users() ([]store.User, error) { return s.st.ListUsers(10000) }

// Access returns a user's SFTP access record (quota override + revoked flag).
func (s *Service) Access(userID int64) (store.FilesAccess, error) {
	return s.st.FilesAccess(userID)
}

// SetQuota sets a per-user quota override (bytes; 0 = server default).
func (s *Service) SetQuota(userID, bytes int64) error { return s.st.SetFilesQuota(userID, bytes) }

// SetRevoked revokes/restores a user's SFTP access (BBS login is unaffected).
func (s *Service) SetRevoked(userID int64, revoked bool) error {
	return s.st.SetFilesRevoked(userID, revoked)
}

// PublicWritable reports whether members may currently write to the public area.
func (s *Service) PublicWritable() bool { return s.publicWritable() }

// PublicList lists the top level of the shared public area for moderation.
func (s *Service) PublicList() ([]Entry, error) {
	des, err := os.ReadDir(s.pubRoot())
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(des))
	for _, de := range des {
		fi, err := de.Info()
		if err != nil {
			continue
		}
		out = append(out, Entry{Name: de.Name(), IsDir: de.IsDir(), Size: fi.Size(), ModTime: fi.ModTime()})
	}
	return out, nil
}

// PublicRemove deletes a top-level entry from the public area (moderation). name
// is treated as a single segment; traversal is rejected.
func (s *Service) PublicRemove(name string) error {
	base := filepath.Base(filepath.Clean("/" + name))
	if base == "." || base == "/" || base == ".." {
		return os.ErrInvalid
	}
	target := filepath.Join(s.pubRoot(), base)
	if !within(s.pubRoot(), target) {
		return os.ErrPermission
	}
	return os.RemoveAll(target)
}

// Kick force-disconnects a live SFTP connection by id. Returns false if unknown.
func (s *Service) Kick(id int64) bool {
	s.reg.mu.Lock()
	c, ok := s.reg.live[id]
	s.reg.mu.Unlock()
	if !ok {
		return false
	}
	if c.closer != nil {
		_ = c.closer()
	}
	return true
}
