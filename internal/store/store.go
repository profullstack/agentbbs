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
	ID        int64
	Name      string
	Kind      string
	PubKeyFP  string
	CreatedAt time.Time
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

	RecordSession(userID int64, username, remote, route string) (int64, error)
	EndSession(sessionID int64) error

	AddScore(userID int64, game string, score int64) error
	TopScores(game string, n int) ([]Score, error)

	// Pod subscription (paid membership, e.g. $1/mo via CoinPay).
	PodPaidUntil(userID int64) (time.Time, bool, error)
	GrantPod(userID int64, until time.Time, paymentRef string) error

	Close() error
}

// ErrKeyMismatch means a username is already registered with another key.
var ErrKeyMismatch = errors.New("username registered with a different key")

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
	return &sqliteStore{db: db}, nil
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
CREATE TABLE IF NOT EXISTS pod_subscriptions (
  user_id INTEGER PRIMARY KEY REFERENCES users(id),
  paid_until TEXT NOT NULL,
  payment_ref TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
`

func (s *sqliteStore) EnsureUser(name, kind, fp string) (User, error) {
	var u User
	var created string
	err := s.db.QueryRow(`SELECT id, name, kind, pubkey_fp, created_at FROM users WHERE name = ?`, name).
		Scan(&u.ID, &u.Name, &u.Kind, &u.PubKeyFP, &created)
	switch {
	case err == sql.ErrNoRows:
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
	u.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return u, nil
}

func (s *sqliteStore) UserByFingerprint(fp string) (User, bool, error) {
	if fp == "" {
		return User{}, false, nil
	}
	var u User
	var created string
	err := s.db.QueryRow(`SELECT id, name, kind, pubkey_fp, created_at FROM users WHERE pubkey_fp = ?`, fp).
		Scan(&u.ID, &u.Name, &u.Kind, &u.PubKeyFP, &created)
	if err == sql.ErrNoRows {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, err
	}
	u.CreatedAt, _ = time.Parse(time.RFC3339, created)
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

func (s *sqliteStore) Close() error { return s.db.Close() }
