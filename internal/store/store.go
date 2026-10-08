package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a match id does not exist.
var ErrNotFound = errors.New("store: not found")

// Store persists matches in SQLite. Each match is kept as a JSON document
// plus a few indexed / mutable columns (tag), which keeps the schema simple
// while the model evolves.
type Store struct {
	db *sql.DB
}

// migrations are applied in order; PRAGMA user_version tracks progress.
var migrations = []string{
	// v1
	`CREATE TABLE IF NOT EXISTS matches (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		guid       TEXT NOT NULL DEFAULT '',
		online     INTEGER NOT NULL DEFAULT 0,
		started_at TEXT NOT NULL,
		ended_at   TEXT NOT NULL,
		mode       TEXT NOT NULL DEFAULT '',
		result     TEXT NOT NULL DEFAULT '',
		tag        TEXT NOT NULL DEFAULT '',
		data       TEXT NOT NULL,
		created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
	);
	CREATE INDEX IF NOT EXISTS idx_matches_started ON matches(started_at);
	CREATE INDEX IF NOT EXISTS idx_matches_guid ON matches(guid);
	CREATE TABLE IF NOT EXISTS kv (k TEXT PRIMARY KEY, v TEXT NOT NULL);`,
}

// Open opens (and creates / migrates) the database at path.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	dsn := "file:" + escapeURIPath(filepath.ToSlash(path)) + "?" + url.Values{
		"_pragma": {"busy_timeout(5000)", "journal_mode(WAL)", "synchronous(NORMAL)", "foreign_keys(1)"},
	}.Encode()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate %s: %w", path, err)
	}
	return s, nil
}

// escapeURIPath escapes the characters SQLite's URI parser would interpret
// in a "file:" name ('%' escapes, '?' query, '#' fragment).
func escapeURIPath(p string) string {
	return strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(p)
}

func (s *Store) migrate() error {
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return err
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

const tsLayout = time.RFC3339

// Save inserts a match (or replaces an existing online match with the same
// guid, e.g. after a reconnect) and sets m.ID. When replacing, the user's tag
// and the original start time are kept, and an "abandoned" record never
// overwrites a finished (win/loss) one.
func (s *Store) Save(ctx context.Context, m *Match) error {
	m.Normalize()
	if m.Online && m.GUID != "" {
		var (
			id                     int64
			tag, startedAt, result string
		)
		err := s.db.QueryRowContext(ctx, `SELECT id, tag, started_at, result FROM matches WHERE guid = ? AND online = 1 ORDER BY id DESC LIMIT 1`, m.GUID).
			Scan(&id, &tag, &startedAt, &result)
		if err == nil {
			m.ID = id
			if m.Result == "abandoned" && result != "abandoned" && result != "" {
				return nil
			}
			if tag != "" {
				m.Tag = tag
			}
			if t, perr := time.Parse(tsLayout, startedAt); perr == nil && t.Before(m.StartedAt) {
				m.StartedAt = t.UTC()
			}
			return s.update(ctx, m)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO matches(guid, online, started_at, ended_at, mode, result, tag, data) VALUES (?,?,?,?,?,?,?,?)`,
		m.GUID, boolInt(m.Online), m.StartedAt.Format(tsLayout), m.EndedAt.Format(tsLayout), m.Mode, m.Result, m.Tag, string(data))
	if err != nil {
		return err
	}
	m.ID, err = res.LastInsertId()
	return err
}

// SaveMany inserts several matches in one transaction (used by seed).
func (s *Store) SaveMany(ctx context.Context, ms []*Match) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO matches(guid, online, started_at, ended_at, mode, result, tag, data) VALUES (?,?,?,?,?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, m := range ms {
		m.Normalize()
		data, err := json.Marshal(m)
		if err != nil {
			tx.Rollback()
			return err
		}
		res, err := stmt.ExecContext(ctx, m.GUID, boolInt(m.Online), m.StartedAt.Format(tsLayout), m.EndedAt.Format(tsLayout), m.Mode, m.Result, m.Tag, string(data))
		if err != nil {
			tx.Rollback()
			return err
		}
		m.ID, _ = res.LastInsertId()
	}
	return tx.Commit()
}

func (s *Store) update(ctx context.Context, m *Match) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE matches SET guid=?, online=?, started_at=?, ended_at=?, mode=?, result=?, tag=?, data=? WHERE id=?`,
		m.GUID, boolInt(m.Online), m.StartedAt.Format(tsLayout), m.EndedAt.Format(tsLayout), m.Mode, m.Result, m.Tag, string(data), m.ID)
	return err
}

// List returns all matches, oldest first.
func (s *Store) List(ctx context.Context) ([]*Match, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, tag, data FROM matches ORDER BY started_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Match{}
	for rows.Next() {
		m, err := scanMatch(rows)
		if err != nil {
			return nil, err
		}
		if m != nil {
			out = append(out, m)
		}
	}
	return out, rows.Err()
}

type scanner interface{ Scan(...any) error }

func scanMatch(r scanner) (*Match, error) {
	var (
		id   int64
		tag  string
		data string
	)
	if err := r.Scan(&id, &tag, &data); err != nil {
		return nil, err
	}
	m := &Match{}
	if err := json.Unmarshal([]byte(data), m); err != nil {
		return nil, nil // corrupt row: skip rather than break the dashboard
	}
	m.ID = id
	m.Tag = tag
	m.Normalize()
	return m, nil
}

// Get returns one match.
func (s *Store) Get(ctx context.Context, id int64) (*Match, error) {
	m, err := scanMatch(s.db.QueryRowContext(ctx, `SELECT id, tag, data FROM matches WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) || (err == nil && m == nil) {
		return nil, ErrNotFound
	}
	return m, err
}

// SetTag changes the tag of a match.
func (s *Store) SetTag(ctx context.Context, id int64, tag string) (*Match, error) {
	m, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	m.Tag = tag
	if err := s.update(ctx, m); err != nil {
		return nil, err
	}
	return m, nil
}

// Delete removes a match.
func (s *Store) Delete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM matches WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Count returns the number of stored matches.
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM matches`).Scan(&n)
	return n, err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ValidTag reports whether a tag value is acceptable.
func ValidTag(t string) bool {
	switch strings.ToLower(t) {
	case "ranked", "casual", "tournament", "private", "other":
		return true
	}
	return false
}
