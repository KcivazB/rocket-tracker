package store

import (
	"context"
	"strings"
	"time"
)

// LeaderboardQuery selects the matches a leaderboard is computed from. Only
// online matches of server accounts count (offline games against bots would
// inflate the numbers).
type LeaderboardQuery struct {
	Mode  string    // "", "all", "1v1", "2v2", "3v3" or "other" (like the dashboard filter)
	Tag   string    // "" / "all" or a tag
	Since time.Time // zero = all time
	// Profiles: the profiles (ProfileKey) that count for each user, the
	// accounts of their settings; users missing from it count all of them.
	Profiles map[int64]map[string]bool
}

// profileExpr is ProfileKey in SQL.
const profileExpr = `CASE WHEN COALESCE(json_extract(data, '$.me.primary_id'), '') <> ''
	THEN lower(trim(json_extract(data, '$.me.primary_id')))
	ELSE CASE WHEN COALESCE(json_extract(data, '$.me.name'), '') <> '' THEN 'name:' || lower(trim(json_extract(data, '$.me.name'))) ELSE '' END END`

// LeaderRow aggregates one player's matches. Averages are over decided
// (won or lost) matches; abandoned ones are only counted.
type LeaderRow struct {
	UserID      int64   `json:"-"`
	Wins        int     `json:"wins"`
	Losses      int     `json:"losses"`
	Abandoned   int     `json:"abandoned"`
	GoalDiffAvg float64 `json:"goal_diff_avg"`
	ScoreAvg    float64 `json:"score_avg"`
	GoalsAvg    float64 `json:"goals_avg"`
	AssistsAvg  float64 `json:"assists_avg"`
	SavesAvg    float64 `json:"saves_avg"`
	ShotsAvg    float64 `json:"shots_avg"`
	MVPs        int     `json:"mvps"`
	LastPlayed  string  `json:"last_played"`
}

// standardModes are the modes the dashboard filter names; the rest is "other".
const standardMode = `(mode IN ('1v1', '2v2', '3v3') AND variant IN ('', 'Soccar'))`

// Leaderboard returns one row per player who has matching matches.
func (s *Store) Leaderboard(ctx context.Context, q LeaderboardQuery) ([]LeaderRow, error) {
	where := []string{`user_id > 0`, `online = 1`, `result IN ('win', 'loss', 'abandoned')`}
	var args []any
	switch q.Mode {
	case "", "all":
	case "other":
		where = append(where, `NOT `+standardMode)
	default:
		where = append(where, `mode = ?`, `variant IN ('', 'Soccar')`)
		args = append(args, q.Mode)
	}
	if q.Tag != "" && q.Tag != "all" {
		where = append(where, `tag = ?`)
		args = append(args, q.Tag)
	}
	if !q.Since.IsZero() {
		where = append(where, `started_at >= ?`)
		args = append(args, q.Since.UTC().Format(tsLayout))
	}
	const dec = `CASE WHEN result IN ('win', 'loss') THEN `
	rows, err := s.db.QueryContext(ctx, `SELECT user_id,
		SUM(result = 'win'), SUM(result = 'loss'), SUM(result = 'abandoned'),
		COALESCE(AVG(`+dec+`team_score - opp_score END), 0),
		COALESCE(AVG(`+dec+`me_score END), 0), COALESCE(AVG(`+dec+`me_goals END), 0),
		COALESCE(AVG(`+dec+`me_assists END), 0), COALESCE(AVG(`+dec+`me_saves END), 0),
		COALESCE(AVG(`+dec+`me_shots END), 0), SUM(mvp), MAX(started_at), `+profileExpr+`
		FROM matches WHERE `+strings.Join(where, " AND ")+` GROUP BY user_id, `+profileExpr+` ORDER BY user_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LeaderRow{}
	for rows.Next() {
		var r LeaderRow
		var profile string
		if err := rows.Scan(&r.UserID, &r.Wins, &r.Losses, &r.Abandoned, &r.GoalDiffAvg, &r.ScoreAvg, &r.GoalsAvg,
			&r.AssistsAvg, &r.SavesAvg, &r.ShotsAvg, &r.MVPs, &r.LastPlayed, &profile); err != nil {
			return nil, err
		}
		if want, ok := q.Profiles[r.UserID]; ok && !want[profile] {
			continue
		}
		if n := len(out); n > 0 && out[n-1].UserID == r.UserID {
			out[n-1].merge(r)
		} else {
			out = append(out, r)
		}
	}
	return out, rows.Err()
}

// merge adds the row of another profile of the same user (averages weighted
// by decided matches).
func (r *LeaderRow) merge(o LeaderRow) {
	a, b := float64(r.Wins+r.Losses), float64(o.Wins+o.Losses)
	avg := func(x, y float64) float64 {
		if a+b == 0 {
			return 0
		}
		return (x*a + y*b) / (a + b)
	}
	r.GoalDiffAvg, r.ScoreAvg, r.GoalsAvg = avg(r.GoalDiffAvg, o.GoalDiffAvg), avg(r.ScoreAvg, o.ScoreAvg), avg(r.GoalsAvg, o.GoalsAvg)
	r.AssistsAvg, r.SavesAvg, r.ShotsAvg = avg(r.AssistsAvg, o.AssistsAvg), avg(r.SavesAvg, o.SavesAvg), avg(r.ShotsAvg, o.ShotsAvg)
	r.Wins, r.Losses, r.Abandoned, r.MVPs = r.Wins+o.Wins, r.Losses+o.Losses, r.Abandoned+o.Abandoned, r.MVPs+o.MVPs
	if o.LastPlayed > r.LastPlayed {
		r.LastPlayed = o.LastPlayed
	}
}

// PlayerSummary is what the players list shows for each account.
type PlayerSummary struct {
	Matches    int      `json:"matches"`
	LastPlayed string   `json:"last_played"`
	GameNames  []string `json:"game_names"` // in-game names seen in the matches, most recent first
}

// PlayerSummaries returns the summary of every account that has matches.
func (s *Store) PlayerSummaries(ctx context.Context) (map[int64]*PlayerSummary, error) {
	out := map[int64]*PlayerSummary{}
	rows, err := s.db.QueryContext(ctx, `SELECT user_id, COUNT(*), MAX(started_at) FROM matches WHERE user_id > 0 GROUP BY user_id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		p := &PlayerSummary{GameNames: []string{}}
		var uid int64
		if err := rows.Scan(&uid, &p.Matches, &p.LastPlayed); err != nil {
			rows.Close()
			return nil, err
		}
		out[uid] = p
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT user_id, name FROM (
			SELECT user_id, COALESCE(json_extract(data, '$.me.name'), '') AS name, MAX(started_at) AS last
			FROM matches WHERE user_id > 0 AND json_valid(data) GROUP BY user_id, name)
		WHERE name != '' ORDER BY user_id, last DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			uid  int64
			name string
		)
		if err := rows.Scan(&uid, &name); err != nil {
			return nil, err
		}
		if p := out[uid]; p != nil && len(p.GameNames) < 5 {
			p.GameNames = append(p.GameNames, name)
		}
	}
	return out, rows.Err()
}
