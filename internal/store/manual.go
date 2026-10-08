package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ManualDay is a number of games entered by hand for one day and mode. They
// are added on top of the tracked matches (they never replace them).
type ManualDay struct {
	Day   string `json:"day"`  // YYYY-MM-DD (local day)
	Mode  string `json:"mode"` // 1v1, 2v2, 3v3, 4v4
	Games int    `json:"games"`
	Wins  int    `json:"wins"`
}

// MaxManualGames bounds a single day/mode entry.
const MaxManualGames = 500

// ErrInvalidManual is returned for out-of-range manual entries.
var ErrInvalidManual = errors.New("store: invalid manual entry")

// ValidManualMode reports whether mode can be entered by hand.
func ValidManualMode(mode string) bool {
	switch mode {
	case "1v1", "2v2", "3v3", "4v4":
		return true
	}
	return false
}

// Validate checks a manual entry.
func (d ManualDay) Validate() error {
	if _, err := time.Parse("2006-01-02", d.Day); err != nil {
		return fmt.Errorf("%w: day must be YYYY-MM-DD", ErrInvalidManual)
	}
	if !ValidManualMode(d.Mode) {
		return fmt.Errorf("%w: mode must be one of 1v1, 2v2, 3v3, 4v4", ErrInvalidManual)
	}
	if d.Games < 0 || d.Games > MaxManualGames {
		return fmt.Errorf("%w: games must be between 0 and %d", ErrInvalidManual, MaxManualGames)
	}
	if d.Wins < 0 || d.Wins > d.Games {
		return fmt.Errorf("%w: wins must be between 0 and games", ErrInvalidManual)
	}
	return nil
}

// ListManual returns the local user's manual entries.
func (s *Store) ListManual(ctx context.Context) ([]ManualDay, error) {
	return s.User(LocalUser).ListManual(ctx)
}

// SetManual stores a manual entry of the local user.
func (s *Store) SetManual(ctx context.Context, d ManualDay) error {
	return s.User(LocalUser).SetManual(ctx, d)
}

// ListManual returns all manual entries ordered by day then mode.
func (sc *Scope) ListManual(ctx context.Context) ([]ManualDay, error) {
	rows, err := sc.s.db.QueryContext(ctx, `SELECT day, mode, games, wins FROM manual_days WHERE user_id = ? ORDER BY day, mode`, sc.uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ManualDay{}
	for rows.Next() {
		var d ManualDay
		if err := rows.Scan(&d.Day, &d.Mode, &d.Games, &d.Wins); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SetManual stores an entry; games == 0 removes it.
func (sc *Scope) SetManual(ctx context.Context, d ManualDay) error {
	if err := d.Validate(); err != nil {
		return err
	}
	if d.Games == 0 {
		_, err := sc.s.db.ExecContext(ctx, `DELETE FROM manual_days WHERE user_id = ? AND day = ? AND mode = ?`, sc.uid, d.Day, d.Mode)
		return err
	}
	_, err := sc.s.db.ExecContext(ctx, `INSERT INTO manual_days (user_id, day, mode, games, wins) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(user_id, day, mode) DO UPDATE SET games = excluded.games, wins = excluded.wins,
		updated_at = strftime('%Y-%m-%dT%H:%M:%SZ','now')`, sc.uid, d.Day, d.Mode, d.Games, d.Wins)
	return err
}
