// Package store is the persistence layer (PRD §4.2): SQLite behind a Store
// interface so a move to Postgres is a driver swap, not a rewrite.
package store

import (
	"database/sql"
	"errors"
	"time"

	_ "modernc.org/sqlite"

	"github.com/profullstack/agentbbs/internal/games"
)

// User is a persisted account (member or agent; guests are never stored).
type User struct {
	ID            int64
	Name          string
	Kind          string
	PubKeyFP      string
	Email         string
	EmailVerified bool
	Premium       bool   // paid the one-time lifetime membership
	PremiumPayID  string // CoinPay payment id of the pending/settled premium charge
	Banned        bool   // suspended by an admin (blocked at login)
	CreatedAt     time.Time
}

// userCols is the column list (in struct order) for every user SELECT, kept in
// sync with scanUser.
const userCols = `id, name, kind, pubkey_fp, email, email_verified, premium, premium_pay_id, banned, created_at`

// scanUser reads one user row selected with userCols.
func scanUser(sc interface{ Scan(...any) error }) (User, error) {
	var u User
	var verified, premium, banned int
	var created string
	if err := sc.Scan(&u.ID, &u.Name, &u.Kind, &u.PubKeyFP, &u.Email, &verified, &premium, &u.PremiumPayID, &banned, &created); err != nil {
		return User{}, err
	}
	u.EmailVerified = verified != 0
	u.Premium = premium != 0
	u.Banned = banned != 0
	u.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return u, nil
}

// Score is one leaderboard entry.
type Score struct {
	User  string
	Game  string
	Score int64
	At    time.Time
}

// Store is the persistence contract shared by all plugins.
type Store interface {
	// EnsureUser returns the user with this name, creating it with the given
	// kind and key fingerprint on first sight. If the name exists with a
	// different fingerprint, ErrKeyMismatch is returned.
	EnsureUser(name, kind, pubkeyFP string) (User, error)
	// UserByFingerprint finds an account by SSH key fingerprint.
	UserByFingerprint(fp string) (User, bool, error)
	// UserByName finds an account by exact username (no creation).
	UserByName(name string) (User, bool, error)
	// LastSeen reports the start of the user's most recent session.
	LastSeen(userID int64) (time.Time, bool, error)

	// SetEmailVerification records the account's email and a fresh
	// confirmation token (a link token or a short code), marking it unverified
	// until the token is consumed.
	SetEmailVerification(userID int64, email, token string) error
	// VerifyEmail consumes a confirmation token: on match it marks the
	// account verified, clears the token, and returns the account.
	VerifyEmail(token string) (User, bool, error)
	// ConfirmEmailCode is the interactive (join@) counterpart to VerifyEmail:
	// it matches the code against the one stored for THIS user (codes are
	// short and not globally unique), and on match marks the account verified
	// and clears the code. Returns ok=false on a wrong/empty code.
	ConfirmEmailCode(userID int64, code string) (User, bool, error)

	// SetPremiumPayment records the CoinPay payment id of a pending premium
	// charge so a later visit can verify whether it settled.
	SetPremiumPayment(userID int64, payID string) error
	// GrantPremium marks the account as a lifetime premium member (the $99
	// Founding Lifetime membership), recording the CoinPay payment reference. Idempotent.
	GrantPremium(userID int64, paymentRef string) error

	RecordSession(userID int64, username, remote, route string) (int64, error)
	EndSession(sessionID int64) error

	AddScore(userID int64, game string, score int64) error
	TopScores(game string, n int) ([]Score, error)

	// Pod subscription (paid membership, e.g. $1/mo via CoinPay).
	PodPaidUntil(userID int64) (time.Time, bool, error)
	GrantPod(userID int64, until time.Time, paymentRef string) error

	// Chat transcripts for the agent@ surface.
	AddChat(userID int64, username, role, text string) error
	RecentChats(username string, n int) ([]ChatMessage, error)

	// Custom domains mapped to a member's homepage (public_html).
	// MapDomain binds domain→username, returning ErrDomainTaken if it is
	// already claimed by someone else (re-binding to the same owner is a no-op).
	MapDomain(domain, username string) error
	UnmapDomain(domain, username string) error
	DomainUser(domain string) (string, bool, error)
	DomainsForUser(username string) ([]string, error)
	AllDomains() ([]DomainMap, error)

	// Admin console (PRD §6). All read-only listings are newest-first.

	// ListUsers returns up to limit accounts, most recently created first.
	ListUsers(limit int) ([]User, error)
	// SetBanned suspends (or restores) an account; banned accounts are blocked
	// at login by the SSH routes.
	SetBanned(userID int64, banned bool) error
	// RecentSessions returns the last n session rows (the audit trail).
	RecentSessions(n int) ([]SessionRow, error)
	// LogAdminAction records one privileged action for the audit log.
	LogAdminAction(admin, action, target, detail string) error
	// RecentAdminActions returns the last n logged admin actions.
	RecentAdminActions(n int) ([]AdminAction, error)
	// RecentChatsAll returns the last n agent@ messages across all users, for
	// moderation review.
	RecentChatsAll(n int) ([]ChatRow, error)
	// DisabledPlugins reports the set of plugin IDs currently switched off.
	DisabledPlugins() (map[string]bool, error)
	// SetPluginDisabled enables or disables a plugin by ID. Idempotent.
	SetPluginDisabled(id string, disabled bool) error

	// AgentGames (PRD §5.2): per-game ELO ladder + replayable match log.

	// Rating returns a player's current rating for a game, or
	// games.DefaultRating if they have no rated history there. It satisfies
	// games.Store so the matchmaker can read ratings.
	Rating(user, game string) (float64, error)
	// SaveMatch records a finished match and upserts both players' ratings.
	// It satisfies games.Store.
	SaveMatch(games.FinishedMatch) error
	// TopRatings returns the n highest-rated players for a game.
	TopRatings(game string, n int) ([]RatingRow, error)
	// RecentMatches returns the last n matches for a game, newest first.
	RecentMatches(game string, n int) ([]MatchRow, error)
	// MatchByID returns one match (with its moves, for replay).
	MatchByID(id int64) (MatchRow, bool, error)

	// MintAPIToken creates and stores a fresh bearer token for the WebSocket
	// game endpoint, bound to username. Returns the token.
	MintAPIToken(username string) (string, error)
	// UserByToken resolves an API token to its account name.
	UserByToken(token string) (string, bool, error)

	// qrypt.chat anonymous-invite issuance (docs/qrypt-invites.md).

	// QryptInviteCount reports how many qrypt.chat invites username has issued.
	QryptInviteCount(username string) (int, error)
	// RecordQryptInvite records one issued invite (its jti, for audit and as
	// the per-member quota counter) against username. It returns
	// ErrQuotaExceeded if the member is already at or above quota.
	RecordQryptInvite(username, jti string, quota int) error

	// News (NNTP) — the members-only Usenet server (docs/news.md).

	// EnsureNewsGroup creates a newsgroup if absent (idempotent), setting the
	// description only on first creation.
	EnsureNewsGroup(name, description string) error
	// NewsGroups lists every group with its article counts, name-sorted.
	NewsGroups() ([]NewsGroup, error)
	// NewsGroup returns one group (with counts), or ok=false if unknown.
	NewsGroup(name string) (NewsGroup, bool, error)
	// NewsArticleByNum fetches an article by its per-group sequence number.
	NewsArticleByNum(group string, num int64) (NewsArticle, bool, error)
	// NewsArticleByMsgID fetches the first article with this Message-ID (any
	// group it was posted to).
	NewsArticleByMsgID(msgID string) (NewsArticle, bool, error)
	// NewsArticlesRange returns articles in [from,to] (inclusive) for a group,
	// ordered by number, for OVER/XOVER.
	NewsArticlesRange(group string, from, to int64) ([]NewsArticle, error)
	// InsertNewsArticle stores an article in a group, assigning the next
	// per-group number, and returns the stored row (with its number).
	InsertNewsArticle(a NewsArticle) (NewsArticle, error)

	// Files (SFTP) — per-user workspaces + the shared public area (docs/files.md).

	// FilesAccess returns a user's SFTP access record (per-user quota override
	// and revoked flag). A user with no row reports the zero value
	// (QuotaBytes 0 = use the server default, Revoked false).
	FilesAccess(userID int64) (FilesAccess, error)
	// SetFilesQuota sets a per-user quota override in bytes (0 clears the
	// override, falling back to the server default). Idempotent upsert.
	SetFilesQuota(userID, bytes int64) error
	// SetFilesRevoked revokes (or restores) a user's SFTP access without
	// touching their BBS login. Idempotent upsert.
	SetFilesRevoked(userID int64, revoked bool) error
	// FilesSetting reads a Files service setting (e.g. the public-write mode).
	FilesSetting(key string) (string, bool, error)
	// SetFilesSetting writes a Files service setting. Idempotent upsert.
	SetFilesSetting(key, value string) error

	Close() error
}

// NewsGroup is a newsgroup plus the cached article-number bounds NNTP clients
// expect (Low/High/Count). Empty groups report Low=1, High=0, Count=0.
type NewsGroup struct {
	Name        string
	Description string
	Posting     bool
	Count       int64
	Low         int64
	High        int64
	CreatedAt   time.Time
}

// NewsArticle is one stored article within a group. Headers beyond these are
// reconstructed at serve time (Message-ID, Newsgroups, Path) from these fields.
type NewsArticle struct {
	Group     string
	Num       int64
	MsgID     string
	Subject   string
	From      string // the From: header (stamped to the posting member)
	Refs      string // the References: header
	Date      string // the Date: header as posted (RFC1123Z)
	Body      string
	Lines     int
	Bytes     int
	CreatedAt time.Time
}

// FilesAccess is a user's SFTP access record. QuotaBytes is a per-user override
// (0 means "use the server default"); Revoked blocks SFTP without affecting the
// BBS login.
type FilesAccess struct {
	QuotaBytes int64
	Revoked    bool
}

// RatingRow is one ladder entry.
type RatingRow struct {
	User   string
	Rating float64
	Played int
}

// MatchRow is a recorded match, including its moves for replay.
type MatchRow struct {
	ID          int64
	Game        string
	P0          string
	P1          string
	Winner      int
	Reason      string
	Moves       []games.Move
	RatingAfter [2]float64
	StartedAt   time.Time
	EndedAt     time.Time
}

// SessionRow is one connection record from the audit trail.
type SessionRow struct {
	ID         int64
	Username   string
	Remote     string
	Route      string
	Started    time.Time
	Ended      time.Time
	EndedValid bool // false while the session is still open
}

// AdminAction is one entry in the admin audit log.
type AdminAction struct {
	Admin  string
	Action string
	Target string
	Detail string
	At     time.Time
}

// ChatRow is one agent@ message with its author, for moderation review.
type ChatRow struct {
	Username string
	Role     string
	Text     string
	At       time.Time
}

// ChatMessage is one line of an agent@ conversation.
type ChatMessage struct {
	Role string // "user" or "agent"
	Text string
	At   time.Time
}

// DomainMap binds a custom domain to a member's homepage (the public_html that
// is also served at /~name). Used to serve, e.g., https://chovy.com from
// users/chovy/public_html.
type DomainMap struct {
	Domain   string
	Username string
	At       time.Time
}

// ErrKeyMismatch means a username is already registered with another key.
var ErrKeyMismatch = errors.New("username registered with a different key")

// ErrDomainTaken means a domain is already mapped to a different member.
var ErrDomainTaken = errors.New("domain already mapped to another account")

// ErrQuotaExceeded means a member has hit their qrypt.chat invite quota.
var ErrQuotaExceeded = errors.New("qrypt invite quota exceeded")

type sqliteStore struct{ db *sql.DB }

// Open opens (and migrates) the SQLite store at path.
func Open(path string) (Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &sqliteStore{db: db}, nil
}

// migrate applies additive schema changes that must not fail on existing
// databases. New columns live here (not in schema) so there is one code path.
func migrate(db *sql.DB) error {
	return ensureColumns(db, "users", [][2]string{
		{"email", "email TEXT NOT NULL DEFAULT ''"},
		{"email_verified", "email_verified INTEGER NOT NULL DEFAULT 0"},
		{"verify_token", "verify_token TEXT NOT NULL DEFAULT ''"},
		{"premium", "premium INTEGER NOT NULL DEFAULT 0"},
		{"premium_ref", "premium_ref TEXT NOT NULL DEFAULT ''"},
		{"premium_pay_id", "premium_pay_id TEXT NOT NULL DEFAULT ''"},
		{"banned", "banned INTEGER NOT NULL DEFAULT 0"},
	})
}

// ensureColumns adds any missing {name, "name TYPE …"} columns to table.
func ensureColumns(db *sql.DB, table string, cols [][2]string) error {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		have[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, c := range cols {
		if !have[c[0]] {
			if _, err := db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + c[1]); err != nil {
				return err
			}
		}
	}
	return nil
}

const schema = `
CREATE TABLE IF NOT EXISTS users (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  kind TEXT NOT NULL,
  pubkey_fp TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE TABLE IF NOT EXISTS sessions (
  id INTEGER PRIMARY KEY,
  user_id INTEGER,
  username TEXT NOT NULL,
  remote_addr TEXT NOT NULL,
  route TEXT NOT NULL,
  started_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ended_at TEXT
);
CREATE TABLE IF NOT EXISTS scores (
  id INTEGER PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id),
  game TEXT NOT NULL,
  score INTEGER NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_scores_game ON scores(game, score DESC);
CREATE TABLE IF NOT EXISTS chat_messages (
  id INTEGER PRIMARY KEY,
  user_id INTEGER,
  username TEXT NOT NULL,
  role TEXT NOT NULL,
  text TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_chat_user ON chat_messages(username, id);
CREATE TABLE IF NOT EXISTS pod_subscriptions (
  user_id INTEGER PRIMARY KEY REFERENCES users(id),
  paid_until TEXT NOT NULL,
  payment_ref TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE TABLE IF NOT EXISTS domains (
  domain TEXT PRIMARY KEY,
  username TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_domains_user ON domains(username);
CREATE TABLE IF NOT EXISTS admin_actions (
  id INTEGER PRIMARY KEY,
  admin TEXT NOT NULL,
  action TEXT NOT NULL,
  target TEXT NOT NULL DEFAULT '',
  detail TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_admin_actions_id ON admin_actions(id DESC);
CREATE TABLE IF NOT EXISTS plugin_state (
  id TEXT PRIMARY KEY,
  disabled INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE TABLE IF NOT EXISTS game_ratings (
  username TEXT NOT NULL,
  game TEXT NOT NULL,
  rating REAL NOT NULL,
  played INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  PRIMARY KEY (username, game)
);
CREATE INDEX IF NOT EXISTS idx_game_ratings_board ON game_ratings(game, rating DESC);
CREATE TABLE IF NOT EXISTS game_matches (
  id INTEGER PRIMARY KEY,
  game TEXT NOT NULL,
  p0 TEXT NOT NULL,
  p1 TEXT NOT NULL,
  winner INTEGER NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  moves TEXT NOT NULL DEFAULT '[]',
  r0_before REAL NOT NULL DEFAULT 0,
  r1_before REAL NOT NULL DEFAULT 0,
  r0_after REAL NOT NULL DEFAULT 0,
  r1_after REAL NOT NULL DEFAULT 0,
  started_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ended_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_game_matches_game ON game_matches(game, id DESC);
CREATE TABLE IF NOT EXISTS api_tokens (
  token TEXT PRIMARY KEY,
  username TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_api_tokens_user ON api_tokens(username);
CREATE TABLE IF NOT EXISTS qrypt_invites (
  jti TEXT PRIMARY KEY,
  username TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_qrypt_invites_user ON qrypt_invites(username);
CREATE TABLE IF NOT EXISTS news_groups (
  name TEXT PRIMARY KEY,
  description TEXT NOT NULL DEFAULT '',
  posting INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE TABLE IF NOT EXISTS news_articles (
  id INTEGER PRIMARY KEY,
  grp TEXT NOT NULL,
  num INTEGER NOT NULL,
  msg_id TEXT NOT NULL,
  subject TEXT NOT NULL DEFAULT '',
  author TEXT NOT NULL DEFAULT '',
  refs TEXT NOT NULL DEFAULT '',
  date TEXT NOT NULL DEFAULT '',
  body TEXT NOT NULL DEFAULT '',
  lines INTEGER NOT NULL DEFAULT 0,
  bytes INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE(grp, num)
);
CREATE INDEX IF NOT EXISTS idx_news_articles_grp ON news_articles(grp, num);
CREATE INDEX IF NOT EXISTS idx_news_articles_msgid ON news_articles(msg_id);
CREATE TABLE IF NOT EXISTS files_access (
  user_id INTEGER PRIMARY KEY,
  quota_bytes INTEGER NOT NULL DEFAULT 0,
  revoked INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE TABLE IF NOT EXISTS files_settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL DEFAULT ''
);
`

func (s *sqliteStore) EnsureUser(name, kind, fp string) (User, error) {
	u, err := scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE name = ?`, name))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, err := s.db.Exec(`INSERT INTO users (name, kind, pubkey_fp) VALUES (?,?,?)`, name, kind, fp)
		if err != nil {
			return User{}, err
		}
		id, _ := res.LastInsertId()
		return User{ID: id, Name: name, Kind: kind, PubKeyFP: fp, CreatedAt: time.Now().UTC()}, nil
	case err != nil:
		return User{}, err
	}
	if u.PubKeyFP != "" && fp != "" && u.PubKeyFP != fp {
		return User{}, ErrKeyMismatch
	}
	return u, nil
}

func (s *sqliteStore) UserByFingerprint(fp string) (User, bool, error) {
	if fp == "" {
		return User{}, false, nil
	}
	u, err := scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE pubkey_fp = ?`, fp))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, err
	}
	return u, true, nil
}

func (s *sqliteStore) SetEmailVerification(userID int64, email, token string) error {
	_, err := s.db.Exec(`UPDATE users SET email = ?, verify_token = ?, email_verified = 0 WHERE id = ?`,
		email, token, userID)
	return err
}

func (s *sqliteStore) VerifyEmail(token string) (User, bool, error) {
	if token == "" {
		return User{}, false, nil
	}
	u, err := scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE verify_token = ?`, token))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, err
	}
	if _, err := s.db.Exec(`UPDATE users SET email_verified = 1, verify_token = '' WHERE id = ?`, u.ID); err != nil {
		return User{}, false, err
	}
	u.EmailVerified = true
	return u, true, nil
}

func (s *sqliteStore) ConfirmEmailCode(userID int64, code string) (User, bool, error) {
	if code == "" {
		return User{}, false, nil
	}
	u, err := scanUser(s.db.QueryRow(
		`SELECT `+userCols+` FROM users WHERE id = ? AND verify_token = ?`, userID, code))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, err
	}
	if _, err := s.db.Exec(`UPDATE users SET email_verified = 1, verify_token = '' WHERE id = ?`, u.ID); err != nil {
		return User{}, false, err
	}
	u.EmailVerified = true
	return u, true, nil
}

func (s *sqliteStore) SetPremiumPayment(userID int64, payID string) error {
	_, err := s.db.Exec(`UPDATE users SET premium_pay_id = ? WHERE id = ?`, payID, userID)
	return err
}

func (s *sqliteStore) GrantPremium(userID int64, paymentRef string) error {
	_, err := s.db.Exec(`UPDATE users SET premium = 1, premium_ref = ? WHERE id = ?`, paymentRef, userID)
	return err
}

func (s *sqliteStore) RecordSession(userID int64, username, remote, route string) (int64, error) {
	var uid any
	if userID > 0 {
		uid = userID
	}
	res, err := s.db.Exec(`INSERT INTO sessions (user_id, username, remote_addr, route) VALUES (?,?,?,?)`,
		uid, username, remote, route)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *sqliteStore) EndSession(id int64) error {
	_, err := s.db.Exec(`UPDATE sessions SET ended_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`, id)
	return err
}

func (s *sqliteStore) AddScore(userID int64, game string, score int64) error {
	_, err := s.db.Exec(`INSERT INTO scores (user_id, game, score) VALUES (?,?,?)`, userID, game, score)
	return err
}

func (s *sqliteStore) TopScores(game string, n int) ([]Score, error) {
	rows, err := s.db.Query(`
		SELECT u.name, s.game, s.score, s.created_at
		FROM scores s JOIN users u ON u.id = s.user_id
		WHERE s.game = ? ORDER BY s.score DESC LIMIT ?`, game, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Score
	for rows.Next() {
		var sc Score
		var at string
		if err := rows.Scan(&sc.User, &sc.Game, &sc.Score, &at); err != nil {
			return nil, err
		}
		sc.At, _ = time.Parse(time.RFC3339, at)
		out = append(out, sc)
	}
	return out, rows.Err()
}

func (s *sqliteStore) PodPaidUntil(userID int64) (time.Time, bool, error) {
	var until string
	err := s.db.QueryRow(`SELECT paid_until FROM pod_subscriptions WHERE user_id = ?`, userID).Scan(&until)
	if err == sql.ErrNoRows {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	t, err := time.Parse(time.RFC3339, until)
	return t, err == nil, err
}

func (s *sqliteStore) GrantPod(userID int64, until time.Time, ref string) error {
	_, err := s.db.Exec(`
		INSERT INTO pod_subscriptions (user_id, paid_until, payment_ref)
		VALUES (?,?,?)
		ON CONFLICT(user_id) DO UPDATE SET
		  paid_until = excluded.paid_until,
		  payment_ref = excluded.payment_ref,
		  updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		userID, until.UTC().Format(time.RFC3339), ref)
	return err
}

func (s *sqliteStore) UserByName(name string) (User, bool, error) {
	u, err := scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, err
	}
	return u, true, nil
}

func (s *sqliteStore) LastSeen(userID int64) (time.Time, bool, error) {
	var at string
	err := s.db.QueryRow(`SELECT started_at FROM sessions WHERE user_id = ? ORDER BY id DESC LIMIT 1`, userID).Scan(&at)
	if err == sql.ErrNoRows {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	t, err := time.Parse(time.RFC3339, at)
	return t, err == nil, err
}

func (s *sqliteStore) AddChat(userID int64, username, role, text string) error {
	var uid any
	if userID > 0 {
		uid = userID
	}
	_, err := s.db.Exec(`INSERT INTO chat_messages (user_id, username, role, text) VALUES (?,?,?,?)`,
		uid, username, role, text)
	return err
}

func (s *sqliteStore) RecentChats(username string, n int) ([]ChatMessage, error) {
	rows, err := s.db.Query(`
		SELECT role, text, created_at FROM (
		  SELECT id, role, text, created_at FROM chat_messages
		  WHERE username = ? ORDER BY id DESC LIMIT ?
		) ORDER BY id ASC`, username, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChatMessage
	for rows.Next() {
		var m ChatMessage
		var at string
		if err := rows.Scan(&m.Role, &m.Text, &at); err != nil {
			return nil, err
		}
		m.At, _ = time.Parse(time.RFC3339, at)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *sqliteStore) MapDomain(domain, username string) error {
	var owner string
	err := s.db.QueryRow(`SELECT username FROM domains WHERE domain = ?`, domain).Scan(&owner)
	switch {
	case err == nil:
		if owner != username {
			return ErrDomainTaken
		}
		return nil // already ours
	case err != sql.ErrNoRows:
		return err
	}
	_, err = s.db.Exec(`INSERT INTO domains (domain, username) VALUES (?,?)`, domain, username)
	return err
}

func (s *sqliteStore) UnmapDomain(domain, username string) error {
	_, err := s.db.Exec(`DELETE FROM domains WHERE domain = ? AND username = ?`, domain, username)
	return err
}

func (s *sqliteStore) DomainUser(domain string) (string, bool, error) {
	var username string
	err := s.db.QueryRow(`SELECT username FROM domains WHERE domain = ?`, domain).Scan(&username)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return username, true, nil
}

func (s *sqliteStore) DomainsForUser(username string) ([]string, error) {
	rows, err := s.db.Query(`SELECT domain FROM domains WHERE username = ? ORDER BY domain`, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *sqliteStore) AllDomains() ([]DomainMap, error) {
	rows, err := s.db.Query(`SELECT domain, username, created_at FROM domains ORDER BY domain`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DomainMap
	for rows.Next() {
		var dm DomainMap
		var at string
		if err := rows.Scan(&dm.Domain, &dm.Username, &at); err != nil {
			return nil, err
		}
		dm.At, _ = time.Parse(time.RFC3339, at)
		out = append(out, dm)
	}
	return out, rows.Err()
}

func (s *sqliteStore) ListUsers(limit int) ([]User, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT `+userCols+` FROM users ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *sqliteStore) SetBanned(userID int64, banned bool) error {
	b := 0
	if banned {
		b = 1
	}
	_, err := s.db.Exec(`UPDATE users SET banned = ? WHERE id = ?`, b, userID)
	return err
}

func (s *sqliteStore) RecentSessions(n int) ([]SessionRow, error) {
	if n <= 0 {
		n = 50
	}
	rows, err := s.db.Query(`
		SELECT id, username, remote_addr, route, started_at, ended_at
		FROM sessions ORDER BY id DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRow
	for rows.Next() {
		var r SessionRow
		var started string
		var ended sql.NullString
		if err := rows.Scan(&r.ID, &r.Username, &r.Remote, &r.Route, &started, &ended); err != nil {
			return nil, err
		}
		r.Started, _ = time.Parse(time.RFC3339, started)
		if ended.Valid && ended.String != "" {
			r.Ended, _ = time.Parse(time.RFC3339, ended.String)
			r.EndedValid = true
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *sqliteStore) LogAdminAction(admin, action, target, detail string) error {
	_, err := s.db.Exec(`INSERT INTO admin_actions (admin, action, target, detail) VALUES (?,?,?,?)`,
		admin, action, target, detail)
	return err
}

func (s *sqliteStore) RecentAdminActions(n int) ([]AdminAction, error) {
	if n <= 0 {
		n = 50
	}
	rows, err := s.db.Query(`
		SELECT admin, action, target, detail, created_at
		FROM admin_actions ORDER BY id DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminAction
	for rows.Next() {
		var a AdminAction
		var at string
		if err := rows.Scan(&a.Admin, &a.Action, &a.Target, &a.Detail, &at); err != nil {
			return nil, err
		}
		a.At, _ = time.Parse(time.RFC3339, at)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *sqliteStore) RecentChatsAll(n int) ([]ChatRow, error) {
	if n <= 0 {
		n = 50
	}
	rows, err := s.db.Query(`
		SELECT username, role, text, created_at
		FROM chat_messages ORDER BY id DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChatRow
	for rows.Next() {
		var c ChatRow
		var at string
		if err := rows.Scan(&c.Username, &c.Role, &c.Text, &at); err != nil {
			return nil, err
		}
		c.At, _ = time.Parse(time.RFC3339, at)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *sqliteStore) DisabledPlugins() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT id FROM plugin_state WHERE disabled = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func (s *sqliteStore) SetPluginDisabled(id string, disabled bool) error {
	d := 0
	if disabled {
		d = 1
	}
	_, err := s.db.Exec(`
		INSERT INTO plugin_state (id, disabled) VALUES (?,?)
		ON CONFLICT(id) DO UPDATE SET
		  disabled = excluded.disabled,
		  updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`, id, d)
	return err
}

func (s *sqliteStore) QryptInviteCount(username string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM qrypt_invites WHERE username = ?`, username).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// RecordQryptInvite atomically enforces the per-member quota and records the
// invite's jti. The count check and the insert run in one transaction so two
// concurrent issuances can't both slip past the cap.
func (s *sqliteStore) RecordQryptInvite(username, jti string, quota int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM qrypt_invites WHERE username = ?`, username).Scan(&n); err != nil {
		return err
	}
	if quota > 0 && n >= quota {
		return ErrQuotaExceeded
	}
	if _, err := tx.Exec(`INSERT INTO qrypt_invites (jti, username) VALUES (?,?)`, jti, username); err != nil {
		return err
	}
	return tx.Commit()
}

// --- Files (SFTP) ------------------------------------------------------------

func (s *sqliteStore) FilesAccess(userID int64) (FilesAccess, error) {
	var fa FilesAccess
	var revoked int
	err := s.db.QueryRow(
		`SELECT quota_bytes, revoked FROM files_access WHERE user_id = ?`, userID,
	).Scan(&fa.QuotaBytes, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return FilesAccess{}, nil
	}
	if err != nil {
		return FilesAccess{}, err
	}
	fa.Revoked = revoked != 0
	return fa, nil
}

func (s *sqliteStore) SetFilesQuota(userID, bytes int64) error {
	_, err := s.db.Exec(`
		INSERT INTO files_access (user_id, quota_bytes, updated_at)
		VALUES (?, ?, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		ON CONFLICT(user_id) DO UPDATE SET
		  quota_bytes = excluded.quota_bytes,
		  updated_at  = excluded.updated_at`, userID, bytes)
	return err
}

func (s *sqliteStore) SetFilesRevoked(userID int64, revoked bool) error {
	r := 0
	if revoked {
		r = 1
	}
	_, err := s.db.Exec(`
		INSERT INTO files_access (user_id, revoked, updated_at)
		VALUES (?, ?, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		ON CONFLICT(user_id) DO UPDATE SET
		  revoked    = excluded.revoked,
		  updated_at = excluded.updated_at`, userID, r)
	return err
}

func (s *sqliteStore) FilesSetting(key string) (string, bool, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM files_settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (s *sqliteStore) SetFilesSetting(key, value string) error {
	_, err := s.db.Exec(`
		INSERT INTO files_settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (s *sqliteStore) Close() error { return s.db.Close() }
