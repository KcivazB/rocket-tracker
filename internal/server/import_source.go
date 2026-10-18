package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"rocket-tracker/internal/store"
)

// importSource deliberately does not use store.Open: an upload is data, not an
// application database to migrate, repair or checkpoint. Keep every statement
// cancellable and stream plain tables rather than materialize the full history.
type importSource struct {
	db          *sql.DB
	columns     map[string]map[string]bool
	repairGoals bool
}

func openImportSource(ctx context.Context, path string) (*importSource, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	sourcePath := filepath.ToSlash(absolute)
	// A Windows drive is a URI path, not an authority (file://C:/...).
	if len(sourcePath) >= 2 && sourcePath[1] == ':' {
		sourcePath = "/" + sourcePath
	}
	u := url.URL{Scheme: "file", Path: sourcePath}
	q := url.Values{"mode": {"ro"}, "_pragma": {"query_only(ON)", "trusted_schema(OFF)"}}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &importSource{db: db, columns: map[string]map[string]bool{}}
	if err = s.validate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *importSource) validate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version < 0 || version > store.SchemaVersion() {
		return fmt.Errorf("unsupported schema version %d", version)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT name,type,sql FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return err
	}
	tables := map[string]bool{}
	for rows.Next() {
		var name, kind string
		var definition sql.NullString
		if err = rows.Scan(&name, &kind, &definition); err != nil {
			break
		}
		if kind == "view" || kind == "trigger" || strings.Contains(strings.ToUpper(definition.String), "CREATE VIRTUAL TABLE") {
			err = fmt.Errorf("unsupported import schema object %q (%s)", name, kind)
			break
		}
		if kind == "table" {
			tables[name] = true
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	// SQLite itself classifies virtual/shadow tables; do not try to parse
	// attacker-controlled CREATE text (comments and whitespace are arbitrary).
	rows, err = s.db.QueryContext(ctx, "PRAGMA table_list")
	if err != nil {
		return err
	}
	for rows.Next() {
		var schema, name, kind string
		var ncol, wr, strict int
		if err = rows.Scan(&schema, &name, &kind, &ncol, &wr, &strict); err != nil {
			break
		}
		if schema == "main" && !strings.HasPrefix(name, "sqlite_") && kind != "table" {
			err = fmt.Errorf("unsupported import table %q (%s)", name, kind)
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, table := range []string{"matches", "manual_days", "trainings", "kv"} {
		if !tables[table] {
			if table == "matches" {
				return fmt.Errorf("missing matches table")
			}
			continue
		}
		// Names come from the fixed allowlist above, never from the uploaded SQL.
		rows, err = s.db.QueryContext(ctx, "PRAGMA table_xinfo('"+table+"')")
		if err != nil {
			return err
		}
		cols := map[string]bool{}
		for rows.Next() {
			var cid, notnull, pk, hidden int
			var name, typ string
			var def any
			if err = rows.Scan(&cid, &name, &typ, &notnull, &def, &pk, &hidden); err != nil {
				break
			}
			if hidden != 0 {
				err = fmt.Errorf("generated/hidden import column %s.%s", table, name)
				break
			}
			cols[name] = true
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return err
		}
		required := []string{"id", "data"}
		if table == "matches" {
			required = append(required, "tag")
		}
		if table == "kv" {
			required = []string{"k", "v"}
		}
		if table == "manual_days" {
			required = []string{"day", "mode", "games", "wins"}
		}
		for _, column := range required {
			if !cols[column] {
				return fmt.Errorf("missing import column %s.%s", table, column)
			}
		}
		s.columns[table] = cols
	}
	s.repairGoals = true
	if _, exists := s.columns["kv"]; exists {
		var marker int
		err := s.db.QueryRowContext(ctx, `SELECT 1 FROM kv NOT INDEXED WHERE k='repair:goals-speeds-v1' LIMIT 1`).Scan(&marker)
		if err == nil {
			s.repairGoals = false
		} else if err != sql.ErrNoRows {
			return err
		}
	}
	return nil
}

func (s *importSource) each(ctx context.Context, table, fields string, visit func(*sql.Rows) error) error {
	cols, ok := s.columns[table]
	if !ok {
		return nil
	}
	where := ""
	if cols["user_id"] {
		where = " WHERE user_id=0"
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+fields+" FROM "+table+" NOT INDEXED"+where)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = visit(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *importSource) matches(ctx context.Context, visit func(*store.Match) error) error {
	return s.each(ctx, "matches", "id,tag,data", func(rows *sql.Rows) error {
		var id int64
		var tag, raw string
		if err := rows.Scan(&id, &tag, &raw); err != nil {
			return err
		}
		m := new(store.Match)
		if json.Unmarshal([]byte(raw), m) != nil {
			return nil
		}
		m.ID = id
		m.Tag = tag
		if s.repairGoals {
			store.RepairImportedGoals(m)
		}
		m.Normalize()
		return visit(m)
	})
}
func (s *importSource) manual(ctx context.Context, visit func(store.ManualDay) error) error {
	return s.each(ctx, "manual_days", "day,mode,games,wins", func(rows *sql.Rows) error {
		var d store.ManualDay
		if err := rows.Scan(&d.Day, &d.Mode, &d.Games, &d.Wins); err != nil {
			return err
		}
		return visit(d)
	})
}
func (s *importSource) trainings(ctx context.Context, visit func(*store.Training) error) error {
	return s.each(ctx, "trainings", "id,data", func(rows *sql.Rows) error {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return err
		}
		t := new(store.Training)
		if json.Unmarshal([]byte(raw), t) != nil {
			return nil
		}
		t.ID = id
		return visit(t)
	})
}
