package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
)

// repairs fix matches stored by earlier versions. Each one runs once per
// database; completion is recorded in the kv table.
var repairs = []struct {
	name string
	fix  func(*Match) bool // reports whether the match changed
}{
	{"repair:goals-speeds-v1", repairGoalsAndSpeeds},
	// Rewriting a match fills its profile column (migration v7) from its
	// data, with the same rules as ProfileKey (not expressible in SQL).
	{"repair:profile-column-v1", func(m *Match) bool { return ProfileKey(m) != "" }},
	{"repair:honor-duels-v1", repairHonorDuel},
}

func (s *Store) runRepairs(ctx context.Context) error {
	for _, r := range repairs {
		var v string
		err := s.db.QueryRowContext(ctx, `SELECT v FROM kv WHERE k = ?`, r.name).Scan(&v)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		ms, err := s.listAll(ctx)
		if err != nil {
			return err
		}
		for _, m := range ms {
			if r.fix(m) {
				m.Normalize()
				if err := update(ctx, s.db, m); err != nil {
					return err
				}
			}
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO kv (k, v) VALUES (?, 'done')`, r.name); err != nil {
			return err
		}
	}
	return nil
}

// Speeds sent by the game are km/h although documented as uu/s; versions
// before 0.3.0 stored them as received. Same thresholds as the tracker.
const (
	uuPerKmh   = 1 / 0.036
	maxCarKmh  = 120.0
	maxBallKmh = 250.0
)

func round1(f float64) float64 { return math.Round(f*10) / 10 }

// repairGoalsAndSpeeds converts km/h speeds to uu/s and merges goals counted
// twice, then recomputes the scores and the scorers' goals from the goals.
func repairGoalsAndSpeeds(m *Match) bool {
	changed := false
	if mv := m.Movement; mv != nil && mv.AvgSpeed > 0 && mv.AvgSpeed <= maxCarKmh {
		mv.AvgSpeed = round1(mv.AvgSpeed * uuPerKmh)
		changed = true
	}
	if h := m.Hits; h != nil && h.MaxSpeed > 0 && h.MaxSpeed <= maxBallKmh {
		h.AvgSpeed = round1(h.AvgSpeed * uuPerKmh)
		h.MaxSpeed = round1(h.MaxSpeed * uuPerKmh)
		changed = true
	}
	gmax := 0.0
	for _, g := range m.Goals {
		gmax = math.Max(gmax, g.Speed)
	}
	if gmax > 0 && gmax <= maxBallKmh {
		for i := range m.Goals {
			m.Goals[i].Speed = round1(m.Goals[i].Speed * uuPerKmh)
		}
		changed = true
	}

	return RepairImportedGoals(m) || changed
}

// RepairImportedGoals repairs the known full/empty duplicate-goal pattern.
// It is deliberately separate from legacy speed heuristics: imported values
// without established units must not be converted from their magnitude.
func RepairImportedGoals(m *Match) bool {
	// The game sends each goal twice (a full GoalScored and an empty one, no
	// scorer, speed 0) at the same game time, and the duplicates also shifted
	// the team attribution. Goals less than 2 s of game time apart are one goal
	// (the clock is stopped until the next kickoff); its team is the scorer's.
	if len(m.Goals) < 2 {
		return false
	}
	teamOf := map[string]string{}
	for _, p := range m.Players {
		side := "them"
		if p.Team == m.MyTeam {
			side = "us"
		}
		teamOf[p.Name] = side
	}
	var kept []Goal
	for _, g := range m.Goals {
		// Only the observed pattern (one of the pair has no scorer) is merged:
		// two full goals are kept, the stored data has no kickoff to tell them apart.
		if n := len(kept); n > 0 && math.Abs(g.T-kept[n-1].T) < 2 && (g.Scorer == "" || kept[n-1].Scorer == "") {
			last := &kept[n-1]
			if last.Scorer == "" {
				last.Scorer, last.Team = g.Scorer, g.Team
			}
			if last.Assister == "" {
				last.Assister = g.Assister
			}
			last.Speed = math.Max(last.Speed, g.Speed)
			continue
		}
		kept = append(kept, g)
	}
	if len(kept) == len(m.Goals) {
		return false
	}
	for i := range kept {
		g := &kept[i]
		if side, ok := teamOf[g.Scorer]; ok {
			g.Team = side
		}
		g.MeScored = g.Scorer != "" && g.Scorer == m.Me.Name && g.Team == "us"
		g.MeAssist = g.Assister != "" && g.Assister == m.Me.Name
	}
	m.Goals = kept
	us, them := 0, 0
	for _, g := range kept {
		switch g.Team {
		case "us":
			us++
		case "them":
			them++
		}
	}
	m.TeamScore, m.OppScore = us, them
	for i := range m.Players {
		p := &m.Players[i]
		scored := 0
		for _, g := range kept {
			if g.Scorer == p.Name && g.Team == teamOf[p.Name] {
				scored++
			}
		}
		if p.Goals > scored {
			p.Score = max(0, p.Score-100*(p.Goals-scored))
			p.Goals = scored
		}
		if p.IsMe {
			m.Me.Goals, m.Me.Score = p.Goals, p.Score
		}
	}
	m.FirstGoal = "" // recomputed by Normalize
	return true
}

// Before v0.22 an Honor Duel (3:00) was saved as a partial 5-minute match:
// every time shifted by the 2 missing minutes. Such a match is recognised by
// its first touch, the kickoff, a few seconds after 2:00 with nothing
// before, and by a complete goal list (a 5-minute match joined at a 3:00
// kickoff misses the goal that preceded it). Matches without touch positions
// (before v0.16) cannot be told apart and stay as they are.
const (
	duelOffset      = 120.0
	duelKickoffSpan = 10.0
)

func repairHonorDuel(m *Match) bool {
	if !m.Partial || m.RegulationS != 0 || m.Hits == nil || len(m.Hits.Points) == 0 ||
		len(m.Goals) != m.TeamScore+m.OppScore || m.DurationS <= duelOffset {
		return false
	}
	first := m.Hits.Points[0].T
	for _, p := range m.Hits.Points {
		first = math.Min(first, p.T)
	}
	if first < duelOffset || first > duelOffset+duelKickoffSpan {
		return false
	}
	for _, g := range m.Goals {
		if g.T < duelOffset {
			return false
		}
	}
	for _, d := range m.Demolitions {
		if d.T < duelOffset {
			return false
		}
	}
	for _, e := range m.Events {
		if e.T < duelOffset {
			return false
		}
	}
	shift := func(t float64) float64 { return round1(t - duelOffset) }
	for i := range m.Goals {
		m.Goals[i].T = shift(m.Goals[i].T)
	}
	for i := range m.Hits.Points {
		m.Hits.Points[i].T = shift(m.Hits.Points[i].T)
	}
	for i := range m.Demolitions {
		m.Demolitions[i].T = shift(m.Demolitions[i].T)
	}
	for i := range m.Events {
		m.Events[i].T = shift(m.Events[i].T)
	}
	m.DurationS = shift(m.DurationS)
	m.Partial, m.RegulationS = false, 180
	return true
}
