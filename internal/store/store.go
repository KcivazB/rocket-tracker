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
	// v2: games entered by hand (played while the tracker wasn't running).
	`CREATE TABLE IF NOT EXISTS manual_days (
		day        TEXT NOT NULL,
		mode       TEXT NOT NULL,
		games      INTEGER NOT NULL,
		wins       INTEGER NOT NULL,
		updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
		PRIMARY KEY (day, mode)
	);`,
	// v3: several users (server mode). Matches and manual games get an owner
	// (0 = the local user), and the stats the leaderboard aggregates become
	// columns.
	`ALTER TABLE matches ADD COLUMN user_id INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE matches ADD COLUMN src_key TEXT NOT NULL DEFAULT '';
	ALTER TABLE matches ADD COLUMN variant TEXT NOT NULL DEFAULT '';
	ALTER TABLE matches ADD COLUMN team_score INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE matches ADD COLUMN opp_score INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE matches ADD COLUMN mvp INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE matches ADD COLUMN me_score INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE matches ADD COLUMN me_goals INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE matches ADD COLUMN me_assists INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE matches ADD COLUMN me_saves INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE matches ADD COLUMN me_shots INTEGER NOT NULL DEFAULT 0;
	UPDATE matches SET
		variant    = COALESCE(json_extract(data, '$.variant'), ''),
		team_score = COALESCE(json_extract(data, '$.team_score'), 0),
		opp_score  = COALESCE(json_extract(data, '$.opp_score'), 0),
		mvp        = COALESCE(json_extract(data, '$.mvp'), 0),
		me_score   = COALESCE(json_extract(data, '$.me.score'), 0),
		me_goals   = COALESCE(json_extract(data, '$.me.goals'), 0),
		me_assists = COALESCE(json_extract(data, '$.me.assists'), 0),
		me_saves   = COALESCE(json_extract(data, '$.me.saves'), 0),
		me_shots   = COALESCE(json_extract(data, '$.me.shots'), 0)
	WHERE json_valid(data);
	CREATE INDEX IF NOT EXISTS idx_matches_user_started ON matches(user_id, started_at);
	CREATE INDEX IF NOT EXISTS idx_matches_user_guid ON matches(user_id, guid);
	CREATE INDEX IF NOT EXISTS idx_matches_user_src ON matches(user_id, src_key);
	CREATE TABLE manual_days_v3 (
		user_id    INTEGER NOT NULL DEFAULT 0,
		day        TEXT NOT NULL,
		mode       TEXT NOT NULL,
		games      INTEGER NOT NULL,
		wins       INTEGER NOT NULL,
		updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
		PRIMARY KEY (user_id, day, mode)
	);
	INSERT INTO manual_days_v3 (user_id, day, mode, games, wins, updated_at)
		SELECT 0, day, mode, games, wins, updated_at FROM manual_days;
	DROP TABLE manual_days;
	ALTER TABLE manual_days_v3 RENAME TO manual_days;
	CREATE TABLE IF NOT EXISTS users (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		oidc_sub      TEXT NOT NULL UNIQUE,
		handle        TEXT NOT NULL UNIQUE,
		name          TEXT NOT NULL DEFAULT '',
		email         TEXT NOT NULL DEFAULT '',
		settings      TEXT NOT NULL DEFAULT '',
		created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
		last_login_at TEXT NOT NULL DEFAULT ''
	);
	CREATE TABLE IF NOT EXISTS sessions (
		token_hash TEXT PRIMARY KEY,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		expires_at TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
	CREATE TABLE IF NOT EXISTS devices (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		name         TEXT NOT NULL,
		token_hash   TEXT NOT NULL UNIQUE,
		created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
		last_seen_at TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX IF NOT EXISTS idx_devices_user ON devices(user_id);`,
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
	if err := s.runRepairs(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: repair %s: %w", path, err)
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

// LocalUser owns every match in local (single-user) mode. Accounts created in
// server mode start at 1.
const LocalUser int64 = 0

// Scope gives access to the matches and manual games of one user.
type Scope struct {
	s   *Store
	uid int64
}

// User returns the data scope of a user.
func (s *Store) User(uid int64) *Scope { return &Scope{s: s, uid: uid} }

// UserID returns the scoped user.
func (sc *Scope) UserID() int64 { return sc.uid }

// The methods below act on LocalUser (local mode).

func (s *Store) Save(ctx context.Context, m *Match) error { return s.User(LocalUser).Save(ctx, m) }
func (s *Store) SaveMany(ctx context.Context, ms []*Match) error {
	return s.User(LocalUser).SaveMany(ctx, ms)
}
func (s *Store) List(ctx context.Context) ([]*Match, error) { return s.User(LocalUser).List(ctx) }
func (s *Store) Get(ctx context.Context, id int64) (*Match, error) {
	return s.User(LocalUser).Get(ctx, id)
}
func (s *Store) SetTag(ctx context.Context, id int64, tag string) (*Match, error) {
	return s.User(LocalUser).SetTag(ctx, id, tag)
}
func (s *Store) Delete(ctx context.Context, id int64) error { return s.User(LocalUser).Delete(ctx, id) }
func (s *Store) Count(ctx context.Context) (int, error)     { return s.User(LocalUser).Count(ctx) }

// matchCols are the columns written for each match, in the order of matchVals.
const matchCols = `guid, online, started_at, ended_at, mode, variant, result, tag, team_score, opp_score, mvp,
	me_score, me_goals, me_assists, me_saves, me_shots, data`

// matchVals returns the column values of a normalized match (see matchCols).
func matchVals(m *Match) ([]any, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return []any{m.GUID, boolInt(m.Online), m.StartedAt.Format(tsLayout), m.EndedAt.Format(tsLayout), m.Mode, m.Variant,
		m.Result, m.Tag, m.TeamScore, m.OppScore, boolInt(m.MVP),
		m.Me.Score, m.Me.Goals, m.Me.Assists, m.Me.Saves, m.Me.Shots, string(data)}, nil
}

const insertMatch = `INSERT INTO matches(user_id, src_key, ` + matchCols + `) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

// Save inserts a match (or replaces an existing online match with the same
// guid, e.g. after a reconnect) and sets m.ID. When replacing, the user's tag
// and the original start time are kept, and an "abandoned" record never
// overwrites a finished (win/loss) one.
func (sc *Scope) Save(ctx context.Context, m *Match) error { return sc.save(ctx, m, "") }

// Ingest saves a match sent by an agent, like Save. srcKey identifies the
// agent's copy of the match: sending it again (a retried upload, an import
// run twice) replaces the stored match instead of adding a duplicate.
func (sc *Scope) Ingest(ctx context.Context, m *Match, srcKey string) error {
	return sc.save(ctx, m, srcKey)
}

func (sc *Scope) save(ctx context.Context, m *Match, srcKey string) error {
	m.Normalize()
	var (
		where string
		arg   any
	)
	switch {
	case m.Online && m.GUID != "":
		where, arg = `guid = ? AND online = 1`, m.GUID
	case srcKey != "":
		where, arg = `src_key = ?`, srcKey
	}
	if where != "" {
		var (
			id                     int64
			tag, startedAt, result string
		)
		err := sc.s.db.QueryRowContext(ctx, `SELECT id, tag, started_at, result FROM matches WHERE user_id = ? AND `+where+` ORDER BY id DESC LIMIT 1`,
			sc.uid, arg).Scan(&id, &tag, &startedAt, &result)
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
			return sc.s.update(ctx, m)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	vals, err := matchVals(m)
	if err != nil {
		return err
	}
	res, err := sc.s.db.ExecContext(ctx, insertMatch, append([]any{sc.uid, srcKey}, vals...)...)
	if err != nil {
		return err
	}
	m.ID, err = res.LastInsertId()
	return err
}

// SaveMany inserts several matches in one transaction (used by seed).
func (sc *Scope) SaveMany(ctx context.Context, ms []*Match) error {
	tx, err := sc.s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, insertMatch)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, m := range ms {
		m.Normalize()
		vals, err := matchVals(m)
		if err != nil {
			tx.Rollback()
			return err
		}
		res, err := stmt.ExecContext(ctx, append([]any{sc.uid, ""}, vals...)...)
		if err != nil {
			tx.Rollback()
			return err
		}
		m.ID, _ = res.LastInsertId()
	}
	return tx.Commit()
}

// update rewrites a match by id; callers have checked its owner.
func (s *Store) update(ctx context.Context, m *Match) error {
	vals, err := matchVals(m)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE matches SET guid=?, online=?, started_at=?, ended_at=?, mode=?, variant=?, result=?, tag=?,
		team_score=?, opp_score=?, mvp=?, me_score=?, me_goals=?, me_assists=?, me_saves=?, me_shots=?, data=? WHERE id=?`,
		append(vals, m.ID)...)
	return err
}

// List returns the user's matches, oldest first.
func (sc *Scope) List(ctx context.Context) ([]*Match, error) {
	return sc.s.list(ctx, `WHERE user_id = ?`, sc.uid)
}

// listAll returns the matches of every user (used by repairs).
func (s *Store) listAll(ctx context.Context) ([]*Match, error) { return s.list(ctx, "") }

func (s *Store) list(ctx context.Context, where string, args ...any) ([]*Match, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, tag, data FROM matches `+where+` ORDER BY started_at ASC, id ASC`, args...)
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

// Get returns one of the user's matches.
func (sc *Scope) Get(ctx context.Context, id int64) (*Match, error) {
	m, err := scanMatch(sc.s.db.QueryRowContext(ctx, `SELECT id, tag, data FROM matches WHERE id = ? AND user_id = ?`, id, sc.uid))
	if errors.Is(err, sql.ErrNoRows) || (err == nil && m == nil) {
		return nil, ErrNotFound
	}
	return m, err
}

// SetTag changes the tag of a match.
func (sc *Scope) SetTag(ctx context.Context, id int64, tag string) (*Match, error) {
	m, err := sc.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	m.Tag = tag
	if err := sc.s.update(ctx, m); err != nil {
		return nil, err
	}
	return m, nil
}

// Delete removes a match.
func (sc *Scope) Delete(ctx context.Context, id int64) error {
	res, err := sc.s.db.ExecContext(ctx, `DELETE FROM matches WHERE id = ? AND user_id = ?`, id, sc.uid)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Count returns the number of the user's matches.
func (sc *Scope) Count(ctx context.Context) (int, error) {
	var n int
	err := sc.s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM matches WHERE user_id = ?`, sc.uid).Scan(&n)
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
