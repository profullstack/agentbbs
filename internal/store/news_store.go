package store

import (
	"database/sql"
	"errors"
	"time"
)

// EnsureNewsGroup creates a newsgroup if it does not already exist. The
// description is only applied on first creation (re-running is a no-op).
func (s *sqliteStore) EnsureNewsGroup(name, description string) error {
	_, err := s.db.Exec(
		`INSERT INTO news_groups (name, description) VALUES (?, ?)
		 ON CONFLICT(name) DO NOTHING`, name, description)
	return err
}

// newsGroupCounts computes Low/High/Count for a group from its articles.
// An empty group reports Low=1, High=0, Count=0 per RFC 3977 convention.
func (s *sqliteStore) newsGroupCounts(name string) (count, low, high int64, err error) {
	row := s.db.QueryRow(
		`SELECT COUNT(*), COALESCE(MIN(num),0), COALESCE(MAX(num),0)
		   FROM news_articles WHERE grp = ?`, name)
	if err = row.Scan(&count, &low, &high); err != nil {
		return 0, 1, 0, err
	}
	if count == 0 {
		low, high = 1, 0
	}
	return count, low, high, nil
}

func (s *sqliteStore) NewsGroup(name string) (NewsGroup, bool, error) {
	var g NewsGroup
	var posting int
	var created string
	err := s.db.QueryRow(
		`SELECT name, description, posting, created_at FROM news_groups WHERE name = ?`, name).
		Scan(&g.Name, &g.Description, &posting, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return NewsGroup{}, false, nil
	}
	if err != nil {
		return NewsGroup{}, false, err
	}
	g.Posting = posting != 0
	g.CreatedAt, _ = time.Parse(time.RFC3339, created)
	if g.Count, g.Low, g.High, err = s.newsGroupCounts(name); err != nil {
		return NewsGroup{}, false, err
	}
	return g, true, nil
}

func (s *sqliteStore) NewsGroups() ([]NewsGroup, error) {
	rows, err := s.db.Query(
		`SELECT name, description, posting, created_at FROM news_groups ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NewsGroup
	for rows.Next() {
		var g NewsGroup
		var posting int
		var created string
		if err := rows.Scan(&g.Name, &g.Description, &posting, &created); err != nil {
			return nil, err
		}
		g.Posting = posting != 0
		g.CreatedAt, _ = time.Parse(time.RFC3339, created)
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Fill counts (a second pass keeps the listing query simple).
	for i := range out {
		if out[i].Count, out[i].Low, out[i].High, err = s.newsGroupCounts(out[i].Name); err != nil {
			return nil, err
		}
	}
	return out, nil
}

const newsArticleCols = `grp, num, msg_id, subject, author, refs, date, body, lines, bytes, created_at`

func scanNewsArticle(sc interface{ Scan(...any) error }) (NewsArticle, error) {
	var a NewsArticle
	var created string
	err := sc.Scan(&a.Group, &a.Num, &a.MsgID, &a.Subject, &a.From, &a.Refs, &a.Date, &a.Body, &a.Lines, &a.Bytes, &created)
	if err != nil {
		return NewsArticle{}, err
	}
	a.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return a, nil
}

func (s *sqliteStore) NewsArticleByNum(group string, num int64) (NewsArticle, bool, error) {
	a, err := scanNewsArticle(s.db.QueryRow(
		`SELECT `+newsArticleCols+` FROM news_articles WHERE grp = ? AND num = ?`, group, num))
	if errors.Is(err, sql.ErrNoRows) {
		return NewsArticle{}, false, nil
	}
	if err != nil {
		return NewsArticle{}, false, err
	}
	return a, true, nil
}

func (s *sqliteStore) NewsArticleByMsgID(msgID string) (NewsArticle, bool, error) {
	a, err := scanNewsArticle(s.db.QueryRow(
		`SELECT `+newsArticleCols+` FROM news_articles WHERE msg_id = ? ORDER BY id LIMIT 1`, msgID))
	if errors.Is(err, sql.ErrNoRows) {
		return NewsArticle{}, false, nil
	}
	if err != nil {
		return NewsArticle{}, false, err
	}
	return a, true, nil
}

func (s *sqliteStore) NewsArticlesRange(group string, from, to int64) ([]NewsArticle, error) {
	rows, err := s.db.Query(
		`SELECT `+newsArticleCols+` FROM news_articles
		  WHERE grp = ? AND num >= ? AND num <= ? ORDER BY num`, group, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NewsArticle
	for rows.Next() {
		a, err := scanNewsArticle(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// InsertNewsArticle assigns the next per-group sequence number atomically and
// stores the article, returning the stored row (with Num populated).
func (s *sqliteStore) InsertNewsArticle(a NewsArticle) (NewsArticle, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return NewsArticle{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var next int64
	if err := tx.QueryRow(
		`SELECT COALESCE(MAX(num),0)+1 FROM news_articles WHERE grp = ?`, a.Group).
		Scan(&next); err != nil {
		return NewsArticle{}, err
	}
	a.Num = next
	if _, err := tx.Exec(
		`INSERT INTO news_articles (grp, num, msg_id, subject, author, refs, date, body, lines, bytes)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		a.Group, a.Num, a.MsgID, a.Subject, a.From, a.Refs, a.Date, a.Body, a.Lines, a.Bytes); err != nil {
		return NewsArticle{}, err
	}
	if err := tx.Commit(); err != nil {
		return NewsArticle{}, err
	}
	return a, nil
}
