package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/profullstack/agentbbs/internal/games"
)

func (s *sqliteStore) Rating(user, game string) (float64, error) {
	var r float64
	err := s.db.QueryRow(`SELECT rating FROM game_ratings WHERE username = ? AND game = ?`, user, game).Scan(&r)
	if errors.Is(err, sql.ErrNoRows) {
		return games.DefaultRating, nil
	}
	if err != nil {
		return games.DefaultRating, err
	}
	return r, nil
}

func (s *sqliteStore) SaveMatch(fm games.FinishedMatch) error {
	moves, err := json.Marshal(fm.Moves)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	if _, err := tx.Exec(`
		INSERT INTO game_matches
		  (game, p0, p1, winner, reason, moves, r0_before, r1_before, r0_after, r1_after, started_at, ended_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		fm.Game, fm.Players[0], fm.Players[1], fm.Winner, fm.Reason, string(moves),
		fm.RatingBefore[0], fm.RatingBefore[1], fm.RatingAfter[0], fm.RatingAfter[1],
		fm.StartedAt.UTC().Format(time.RFC3339), fm.EndedAt.UTC().Format(time.RFC3339),
	); err != nil {
		return err
	}
	for i := 0; i < 2; i++ {
		if err := upsertRating(tx, fm.Players[i], fm.Game, fm.RatingAfter[i]); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func upsertRating(tx *sql.Tx, user, game string, rating float64) error {
	_, err := tx.Exec(`
		INSERT INTO game_ratings (username, game, rating, played) VALUES (?,?,?,1)
		ON CONFLICT(username, game) DO UPDATE SET
		  rating = excluded.rating,
		  played = game_ratings.played + 1,
		  updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`, user, game, rating)
	return err
}

func (s *sqliteStore) TopRatings(game string, n int) ([]RatingRow, error) {
	if n <= 0 {
		n = 20
	}
	rows, err := s.db.Query(`
		SELECT username, rating, played FROM game_ratings
		WHERE game = ? ORDER BY rating DESC, played DESC LIMIT ?`, game, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RatingRow
	for rows.Next() {
		var r RatingRow
		if err := rows.Scan(&r.User, &r.Rating, &r.Played); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// matchCols is the column list (in struct order) for match SELECTs.
const matchCols = `id, game, p0, p1, winner, reason, moves, r0_after, r1_after, started_at, ended_at`

func scanMatch(sc interface{ Scan(...any) error }) (MatchRow, error) {
	var m MatchRow
	var movesJSON, started, ended string
	if err := sc.Scan(&m.ID, &m.Game, &m.P0, &m.P1, &m.Winner, &m.Reason,
		&movesJSON, &m.RatingAfter[0], &m.RatingAfter[1], &started, &ended); err != nil {
		return MatchRow{}, err
	}
	_ = json.Unmarshal([]byte(movesJSON), &m.Moves)
	m.StartedAt, _ = time.Parse(time.RFC3339, started)
	m.EndedAt, _ = time.Parse(time.RFC3339, ended)
	return m, nil
}

func (s *sqliteStore) RecentMatches(game string, n int) ([]MatchRow, error) {
	if n <= 0 {
		n = 20
	}
	rows, err := s.db.Query(`SELECT `+matchCols+` FROM game_matches WHERE game = ? ORDER BY id DESC LIMIT ?`, game, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MatchRow
	for rows.Next() {
		m, err := scanMatch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *sqliteStore) MatchByID(id int64) (MatchRow, bool, error) {
	m, err := scanMatch(s.db.QueryRow(`SELECT `+matchCols+` FROM game_matches WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return MatchRow{}, false, nil
	}
	if err != nil {
		return MatchRow{}, false, err
	}
	return m, true, nil
}

func (s *sqliteStore) MintAPIToken(username string) (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b[:])
	if _, err := s.db.Exec(`INSERT INTO api_tokens (token, username) VALUES (?,?)`, token, username); err != nil {
		return "", err
	}
	return token, nil
}

func (s *sqliteStore) UserByToken(token string) (string, bool, error) {
	if token == "" {
		return "", false, nil
	}
	var username string
	err := s.db.QueryRow(`SELECT username FROM api_tokens WHERE token = ?`, token).Scan(&username)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return username, true, nil
}
