package pilot

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

var (
	errRevisionConflict = errors.New("status revision conflicts with stored data")
	errRevisionStale    = errors.New("status revision is older than stored data")
)

// Status is the only persisted operational object in pilot mode.
type Status struct {
	Subject    string    `json:"subject"`
	Audience   string    `json:"audience"`
	State      string    `json:"state"`
	Summary    string    `json:"summary"`
	ObservedAt time.Time `json:"observed_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Revision   int64     `json:"revision"`
}

// StatusView adds the derived freshness value returned to a query caller.
type StatusView struct {
	Status
	Stale bool `json:"stale"`
}

type Store struct {
	db *sql.DB
}

func ensurePrivateDir(path string) error {
	if path == "" || filepath.Clean(path) == "." {
		return errors.New("refusing to use the current directory")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("path is a symlink")
	}
	if !info.IsDir() {
		return errors.New("path is not a real directory")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return err
	}
	info, err = os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("directory permissions %04o are too broad", info.Mode().Perm())
	}
	return nil
}

// Backup creates a consistent SQLite snapshot using SQLite's backup-capable
// VACUUM INTO operation. The source stays open; the destination must be a new,
// private regular file path.
func (s *Store) Backup(path string) error {
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("backup directory: %w", err)
	}
	if _, err := os.Lstat(path); err == nil {
		return errors.New("backup destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := s.db.Exec(`VACUUM INTO ?`, path); err != nil {
		return fmt.Errorf("backup pilot database: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("protect pilot backup: %w", err)
	}
	return nil
}

// Restore verifies a stopped pilot database snapshot before copying it over
// the configured path. Callers must stop the pilot first; this function refuses
// to operate on an open Store and keeps replacement atomic with a same-directory
// temporary file.
func Restore(source, destination string) error {
	if os.Geteuid() == 0 {
		return errors.New("pilot refuses to restore as root")
	}
	if err := validatePrivateFile(source); err != nil {
		return fmt.Errorf("restore source: %w", err)
	}
	if err := ensurePrivateDir(filepath.Dir(destination)); err != nil {
		return fmt.Errorf("restore destination directory: %w", err)
	}
	if _, err := os.Lstat(destination); err == nil {
		return errors.New("restore destination must be absent; retain the old database and move it aside first")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()
	var header [16]byte
	if _, err := io.ReadFull(src, header[:]); err != nil {
		return fmt.Errorf("read restore source: %w", err)
	}
	if string(header[:6]) != "SQLite" {
		return errors.New("restore source is not SQLite")
	}
	checkDB, err := sql.Open("sqlite", source+"?mode=ro")
	if err != nil {
		return fmt.Errorf("open restore source: %w", err)
	}
	var integrity string
	err = checkDB.QueryRow(`PRAGMA integrity_check`).Scan(&integrity)
	closeErr := checkDB.Close()
	if err != nil {
		return fmt.Errorf("check restore source integrity: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("close restore source: %w", closeErr)
	}
	if integrity != "ok" {
		return fmt.Errorf("restore source integrity check: %s", integrity)
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".pilot-restore-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		tmp.Close()
		return err
	}
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		return fmt.Errorf("copy restore source: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, destination)
}

const pilotSchema = `
CREATE TABLE IF NOT EXISTS statuses (
  audience TEXT NOT NULL,
  subject TEXT NOT NULL,
  state TEXT NOT NULL,
  summary TEXT NOT NULL,
  observed_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  revision INTEGER NOT NULL,
  payload TEXT NOT NULL,
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  PRIMARY KEY (audience, subject)
);
`

// Open opens a dedicated pilot database. It intentionally does not use the
// normal AgentBBS store or schema. The database and parent directory are
// private to the process; deployment still must prove the process is non-root
// and the volume is isolated/read-only except for this database.
func Open(path string) (*Store, error) {
	if os.Geteuid() == 0 {
		return nil, errors.New("pilot refuses to run as root")
	}
	if path == "" {
		return nil, errors.New("pilot database path is empty")
	}
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("pilot database directory: %w", err)
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, errors.New("pilot database must be a regular file")
		}
		if info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("pilot database permissions %04o are too broad", info.Mode().Perm())
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect pilot database: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create pilot database: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("close pilot database handle: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("protect pilot database: %w", err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open pilot database: %w", err)
	}
	// One writer connection keeps revision comparison and update atomic for the
	// small pilot and avoids an application-level lock around SQLite state.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(pilotSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize pilot database: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Publish stores a status only if its revision is newer, or if the exact same
// revision/payload is replayed. A same-revision different payload is a hard
// conflict; this prevents two writers from silently forking state.
func (s *Store) Publish(status Status) error {
	payload, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("marshal status: %w", err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var revision int64
	var storedPayload string
	err = tx.QueryRow(`SELECT revision, payload FROM statuses WHERE audience=? AND subject=?`, status.Audience, status.Subject).Scan(&revision, &storedPayload)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.Exec(`INSERT INTO statuses (audience, subject, state, summary, observed_at, expires_at, revision, payload) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			status.Audience, status.Subject, status.State, status.Summary, status.ObservedAt.UTC().Format(time.RFC3339Nano), status.ExpiresAt.UTC().Format(time.RFC3339Nano), status.Revision, string(payload))
	case err != nil:
		return err
	case status.Revision < revision:
		return errRevisionStale
	case status.Revision == revision && string(payload) != storedPayload:
		return errRevisionConflict
	case status.Revision == revision:
		return tx.Commit()
	default:
		_, err = tx.Exec(`UPDATE statuses SET state=?, summary=?, observed_at=?, expires_at=?, revision=?, payload=?, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE audience=? AND subject=?`,
			status.State, status.Summary, status.ObservedAt.UTC().Format(time.RFC3339Nano), status.ExpiresAt.UTC().Format(time.RFC3339Nano), status.Revision, string(payload), status.Audience, status.Subject)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Query returns the selected audience only. Stale is derived at read time from
// expires_at and is never persisted as authoritative state.
func (s *Store) Query(audience, subject string, now time.Time, limit int) ([]StatusView, error) {
	if limit <= 0 {
		return nil, errors.New("query limit must be positive")
	}
	q := `SELECT subject, audience, state, summary, observed_at, expires_at, revision FROM statuses WHERE audience=?`
	args := []any{audience}
	if subject != "" {
		q += ` AND subject=?`
		args = append(args, subject)
	}
	q += ` ORDER BY subject LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StatusView
	for rows.Next() {
		var status Status
		var observed, expires string
		if err := rows.Scan(&status.Subject, &status.Audience, &status.State, &status.Summary, &observed, &expires, &status.Revision); err != nil {
			return nil, err
		}
		status.ObservedAt, err = time.Parse(time.RFC3339Nano, observed)
		if err != nil {
			return nil, err
		}
		status.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires)
		if err != nil {
			return nil, err
		}
		out = append(out, StatusView{Status: status, Stale: !now.Before(status.ExpiresAt)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
