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
	"strconv"
	"strings"
	"time"

	"rocket-tracker/internal/domain"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a match id does not exist.
var ErrNotFound = errors.New("store: not found")

// Store persists matches in SQLite. Each match is kept as a JSON document
// plus a few indexed / mutable columns (tag), which keeps the schema simple
// while the model evolves.
type Store struct {
	db  *sql.DB
	rev *revisions
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
	`CREATE TABLE IF NOT EXISTS ranks (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id    INTEGER NOT NULL DEFAULT 0,
		at         TEXT NOT NULL,
		playlist   TEXT NOT NULL,
		tier       INTEGER NOT NULL DEFAULT -1,
		division   INTEGER NOT NULL DEFAULT 0,
		mmr        INTEGER NOT NULL DEFAULT -1
	);
	CREATE INDEX IF NOT EXISTS idx_ranks_user_at ON ranks(user_id, at);`,
	`ALTER TABLE ranks ADD COLUMN profile TEXT NOT NULL DEFAULT '';`,
	// v6: trainings (freeplay, packs, warm-up timer).
	`CREATE TABLE IF NOT EXISTS trainings (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id    INTEGER NOT NULL DEFAULT 0,
		src_key    TEXT NOT NULL DEFAULT '',
		started_at TEXT NOT NULL,
		ended_at   TEXT NOT NULL,
		data       TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_trainings_user_started ON trainings(user_id, started_at);`,
	// v7: the account a match was played on (ProfileKey), part of its
	// identity: two accounts of one user in the same online game are two
	// matches. Filled for existing rows by the "profile-column" repair.
	`ALTER TABLE matches ADD COLUMN profile TEXT NOT NULL DEFAULT '';
	CREATE INDEX IF NOT EXISTS idx_matches_user_guid_profile ON matches(user_id, guid, profile);`,
	// v8: recording and delivery intent share a transaction. Unknown identities
	// remain held and are never silently rebound to the current credentials.
	`CREATE TABLE delivery_intents (
 id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL,
 kind TEXT NOT NULL, record_id INTEGER NOT NULL, destination TEXT NOT NULL,
 delivery_key TEXT NOT NULL, revision INTEGER NOT NULL DEFAULT 1,
 payload TEXT NOT NULL, state TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '',
 UNIQUE(user_id,kind,record_id,destination));
 CREATE INDEX delivery_pending ON delivery_intents(user_id,destination,state,id);
 CREATE TABLE legacy_deliveries (source TEXT PRIMARY KEY, data BLOB NOT NULL,
 state TEXT NOT NULL DEFAULT 'held', reason TEXT NOT NULL);`,
	// v9: immutable origin survives acknowledgement of the transient intent.
	`CREATE TABLE record_origins (
 user_id INTEGER NOT NULL, kind TEXT NOT NULL, record_id INTEGER NOT NULL,
 destination TEXT NOT NULL, PRIMARY KEY(user_id,kind,record_id));
 INSERT INTO record_origins(user_id,kind,record_id,destination)
 SELECT user_id,kind,record_id,CASE WHEN COUNT(DISTINCT destination)=1 THEN MIN(destination) ELSE '' END
 FROM delivery_intents GROUP BY user_id,kind,record_id;`,
	// v10: retain every merged sender key for replay after a bounded split.
	`CREATE TABLE IF NOT EXISTS training_sources (
 user_id INTEGER NOT NULL, src_key TEXT NOT NULL, record_id INTEGER NOT NULL,
 PRIMARY KEY(user_id,src_key,record_id));
 INSERT OR IGNORE INTO training_sources SELECT user_id,src_key,id FROM trainings WHERE src_key!='';`,
}

// Open opens (and creates / migrates) the database at path.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	dsn := "file:" + escapeURIPath(filepath.ToSlash(path)) + "?" + url.Values{
		"_pragma": {"busy_timeout(5000)", "journal_mode(WAL)", "synchronous(FULL)", "foreign_keys(1)"},
	}.Encode()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, rev: newRevisions()}
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
	if v < 0 || v > len(migrations) {
		return fmt.Errorf("unsupported schema version %d (supported 0..%d)", v, len(migrations))
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

// Checkpoint copies the WAL into the database file and empties it, so the .db
// file alone holds every write. The desktop app is usually killed rather than
// closed, and its WAL never grows enough for SQLite to checkpoint on its own.
func (s *Store) Checkpoint(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	return err
}

// Close checkpoints then closes the database.
func (s *Store) Close() error {
	return errors.Join(s.Checkpoint(context.Background()), s.db.Close())
}

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
	me_score, me_goals, me_assists, me_saves, me_shots, profile, data`

// matchVals returns the column values of a normalized match (see matchCols).
func matchVals(m *Match) ([]any, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return []any{m.GUID, boolInt(m.Online), m.StartedAt.Format(tsLayout), m.EndedAt.Format(tsLayout), m.Mode, m.Variant,
		m.Result, m.Tag, m.TeamScore, m.OppScore, boolInt(m.MVP),
		m.Me.Score, m.Me.Goals, m.Me.Assists, m.Me.Saves, m.Me.Shots, ProfileKey(m), string(data)}, nil
}

const insertMatch = `INSERT INTO matches(user_id, src_key, ` + matchCols + `) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

// Save inserts a match (or replaces the stored copy of the same match, e.g.
// after a reconnect; see findSame) and sets m.ID. When replacing, the user's
// tag and the original start time are kept, and an "abandoned" record never
// overwrites a finished (win/loss) one.
func (sc *Scope) Save(ctx context.Context, m *Match) error { return sc.save(ctx, m, "") }

// Ingest saves a match sent by an agent, like Save. srcKey identifies the
// agent's copy of the match: sending it again (a retried upload, an import
// run twice) replaces the stored match instead of adding a duplicate.
func (sc *Scope) Ingest(ctx context.Context, m *Match, srcKey string) error {
	return sc.save(ctx, m, srcKey)
}

func (sc *Scope) save(ctx context.Context, m *Match, srcKey string) error {
	return sc.saveWithDelivery(ctx, m, srcKey, nil, false)
}

func (sc *Scope) saveWithDelivery(ctx context.Context, m *Match, srcKey string, destination *domain.Destination, local bool) error {
	repairHonorDuel(m) // still sent that way by agents before v0.22
	m.Normalize()
	// Lookup and write in one transaction: the same match uploaded twice at
	// once (an agent retry racing the first attempt) must not be inserted twice.
	tx, err := sc.s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var (
		id                     int64
		tag, startedAt, result string
	)
	where, args := findSame(m, srcKey)
	origin := recordingOrigin(destination, local)
	where = "COALESCE((SELECT o.destination FROM record_origins o WHERE o.user_id=matches.user_id AND o.kind='match' AND o.record_id=matches.id),'')=? AND " + where
	args = append([]any{origin}, args...)

	err = tx.QueryRowContext(ctx, `SELECT id, tag, started_at, result FROM matches WHERE user_id = ? AND `+where,
		append([]any{sc.uid}, args...)...).Scan(&id, &tag, &startedAt, &result)
	switch {
	case err == nil:
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
		err = update(ctx, tx, m)
	case errors.Is(err, sql.ErrNoRows):
		var vals []any
		if vals, err = matchVals(m); err != nil {
			return err
		}
		var res sql.Result
		if res, err = tx.ExecContext(ctx, insertMatch, append([]any{sc.uid, srcKey}, vals...)...); err == nil {
			m.ID, err = res.LastInsertId()
		}
	}
	if err != nil {
		return err
	}
	if err := sc.persistOrigin(ctx, tx, "match", m.ID, origin); err != nil {
		return err
	}
	if destination != nil {
		if err := sc.queueDelivery(ctx, tx, "match", m.ID, m, destination); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	sc.s.touch(sc.uid)
	return nil
}

// findSame returns the condition (and its ORDER BY / LIMIT) selecting the
// stored copy of m. A match is one game seen from one account (ProfileKey):
//   - online: its guid; two accounts of the user in the same game are two
//     matches;
//   - offline: its guid ("local-..." from the tracker) and exact start time,
//     or the sender's srcKey. The live upload (random key) and a later import
//     of the local database (ImportKey) are thus the same match.
//
// Rows without a known account (stored before the profile column, or
// without "me") match any account, and a match without one matches any row.
func findSame(m *Match, srcKey string) (string, []any) {
	profile := ProfileKey(m)
	same, args := `guid = ? AND online = 1`, []any{m.GUID}
	if !m.Online || m.GUID == "" {
		same, args = `guid = ? AND online = ? AND started_at = ?`, []any{m.GUID, boolInt(m.Online), m.StartedAt.Format(tsLayout)}
	}
	if profile != "" {
		same += ` AND profile IN (?, '')`
		args = append(args, profile)
	}
	order, oargs := `profile = ? DESC, id DESC`, []any{profile}
	if (!m.Online || m.GUID == "") && srcKey != "" {
		same = `(` + same + `) OR (src_key = ?`
		if profile != "" {
			same += ` AND profile IN (?, '')`
		}
		same += `)`
		args = append(args, srcKey)
		if profile != "" {
			args = append(args, profile)
		}
		order, oargs = `src_key = ? DESC, `+order, append([]any{srcKey}, oargs...)
	}
	return `(` + same + `) ORDER BY ` + order + ` LIMIT 1`, append(args, oargs...)
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
		repairHonorDuel(m)
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
	if err := tx.Commit(); err != nil {
		return err
	}
	sc.s.touch(sc.uid)
	return nil
}

// execer is a database or a transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// update rewrites a match by id; callers have checked its owner.
func update(ctx context.Context, db execer, m *Match) error {
	vals, err := matchVals(m)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `UPDATE matches SET guid=?, online=?, started_at=?, ended_at=?, mode=?, variant=?, result=?, tag=?,
		team_score=?, opp_score=?, mvp=?, me_score=?, me_goals=?, me_assists=?, me_saves=?, me_shots=?, profile=?, data=? WHERE id=?`,
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
	if err := update(ctx, sc.s.db, m); err != nil {
		return nil, err
	}
	sc.s.touch(sc.uid)
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
	sc.s.touch(sc.uid)
	return nil
}

// DeleteProfile removes one of the user's accounts (see ProfileKey): its
// matches, the rank entries noted for it and its trainings, e.g. a player
// recorded while spectating. It returns the number of matches removed.
func (sc *Scope) DeleteProfile(ctx context.Context, key string) (int, error) {
	ms, err := sc.List(ctx)
	if err != nil {
		return 0, err
	}
	tx, err := sc.s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n := 0
	for _, m := range ms {
		if ProfileKey(m) != key {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM matches WHERE id = ? AND user_id = ?`, m.ID, sc.uid); err != nil {
			return 0, err
		}
		n++
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM ranks WHERE user_id = ? AND profile = ?`, sc.uid, key); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM trainings WHERE user_id = ? AND json_extract(data, '$.profile') = ?`, sc.uid, key); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	sc.s.touch(sc.uid)
	return n, nil
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

// ImportKey identifies a match of a local database once imported on a server
// (`rltracker agent import`, or a file import), so that importing it again
// updates instead of duplicating.
func ImportKey(m *Match) string {
	return "import:" + m.GUID + ":" + strconv.FormatInt(m.StartedAt.Unix(), 10)
}
