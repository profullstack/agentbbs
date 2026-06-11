// Package store is the persistence layer (PRD §4.2): SQLite behind a Store
// interface so a move to Postgres is a driver swap, not a rewrite.
package store

import (
	"database/sql"
	"errors"
	"time"

	_ "modernc.org/sqlite"
)

// User is a persisted account (member or agent; guests are never stored).
type User struct {
	ID            int64
	Name          string
	Kind          string
	PubKeyFP      string
	Email         string
	EmailVerified bool
	CreatedAt     time.Time
}

// userCols is the column list (in struct order) for every user SELECT, kept in
// sync with scanUser.
const userCols = `id, name, kind, pubkey_fp, email, email_verified, created_at`

// scanUser reads one user row selected with userCols.
func scanUser(sc interface{ Scan(...any) error }) (User, error) {
	var u User
	var verified int
	var created string
	if err := sc.Scan(&u.ID, &u.Name, &u.Kind, &u.PubKeyFP, &u.Email, &verified, &created); err != nil {
		return User{}, err
	}
	u.EmailVerified = verified != 0
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
	// confirmation token, marking it unverified until the token is used.
	SetEmailVerification(userID int64, email, token string) error
	// VerifyEmail consumes a confirmation token: on match it marks the
	// account verified, clears the token, and returns the account.
	VerifyEmail(token string) (User, bool, error)

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

	Close() error
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

func (s *sqliteStore) Close() error { return s.db.Close() }
