// Package domain defines shared match values and pure profile rules.
package domain

import (
	"strings"
	"time"
)

// Match is the JSON model served by GET /api/matches (see docs/SPEC.md §4).
type Match struct {
	// SpeedUnit applies to movement, hits and goals. New unknown units retain
	// raw values; absent means the historical uu/s contract.
	SpeedUnit SpeedUnit      `json:"speed_unit,omitempty"`
	ID        int64          `json:"id"`
	GUID      string         `json:"guid"`
	Online    bool           `json:"online"`
	StartedAt time.Time      `json:"started_at"`
	EndedAt   time.Time      `json:"ended_at"`
	DurationS float64        `json:"duration_s"`
	Overtime  bool           `json:"overtime"`
	OvertimeS float64        `json:"overtime_s"`
	Arena     string         `json:"arena"`
	Mode      string         `json:"mode"`
	TeamSize  int            `json:"team_size"`
	Variant   string         `json:"variant"`
	Tag       string         `json:"tag"`
	Result    string         `json:"result"` // win | loss | abandoned
	Forfeit   bool           `json:"forfeit"`
	Partial   bool           `json:"partial"` // tracking started mid-match (earlier events missed)
	MyTeam    int            `json:"my_team"`
	TeamScore int            `json:"team_score"`
	OppScore  int            `json:"opp_score"`
	GoalDiff  int            `json:"goal_diff"`
	FirstGoal string         `json:"first_goal"` // us | them | none
	MVP       bool           `json:"mvp"`
	Me        MeStats        `json:"me"`
	Players   []Player       `json:"players"`
	Movement  *Movement      `json:"movement"`
	Hits      *Hits          `json:"hits"`
	Goals     []Goal         `json:"goals"`
	Statfeed  map[string]int `json:"statfeed"`
	// Demolitions involving me (older matches have none).
	Demolitions []Demolition `json:"demolitions,omitempty"`
	// Events are the timed shots and saves of both teams (older matches have none).
	Events []Event `json:"events,omitempty"`
	// RegulationS is the regulation length seen from the start: 300, or 180 for
	// an Honor Duel. 0 when partial or recorded before v0.22.
	RegulationS int `json:"regulation_s,omitempty"`
}

// Event is a shot or a save of the match, by either team, at game time T (s,
// overtime included). A goal is a shot too: the game sends both.
type Event struct {
	T    float64 `json:"t"`
	Kind string  `json:"kind"` // shot | save
	Team string  `json:"team"` // us | them
	Me   bool    `json:"me,omitempty"`
}

// EventKind maps a statfeed event name to an Event kind ("" when not one).
func EventKind(name string) string {
	switch name {
	case "Shot":
		return "shot"
	case "Save", "EpicSave":
		return "save"
	}
	return ""
}

type MeStats struct {
	Name      string `json:"name"`
	PrimaryID string `json:"primary_id"`
	Score     int    `json:"score"`
	Goals     int    `json:"goals"`
	Shots     int    `json:"shots"`
	Assists   int    `json:"assists"`
	Saves     int    `json:"saves"`
	Touches   int    `json:"touches"`
	Demos     int    `json:"demos"`
}

type Player struct {
	Name      string `json:"name"`
	PrimaryID string `json:"primary_id"`
	Team      int    `json:"team"`
	Score     int    `json:"score"`
	Goals     int    `json:"goals"`
	Shots     int    `json:"shots"`
	Assists   int    `json:"assists"`
	Saves     int    `json:"saves"`
	Touches   int    `json:"touches"`
	Demos     int    `json:"demos"`
	IsMe      bool   `json:"is_me"`
}

type Movement struct {
	// CarSampleS is accepted complete car time. Nil denotes a legacy record;
	// zero denotes observed demolition only, with unavailable car aggregates.
	CarSampleS    *float64 `json:"car_sample_s,omitempty"`
	SampleS       float64  `json:"sample_s"`
	AvgSpeed      float64  `json:"avg_speed"`
	SupersonicPct float64  `json:"supersonic_pct"`
	GroundPct     float64  `json:"ground_pct"`
	WallPct       float64  `json:"wall_pct"`
	AirPct        float64  `json:"air_pct"`
	AvgBoost      float64  `json:"avg_boost"`
	ZeroBoostPct  float64  `json:"zero_boost_pct"`
	FullBoostPct  float64  `json:"full_boost_pct"`
	BoostingPct   float64  `json:"boosting_pct"`
	PowerslidePct float64  `json:"powerslide_pct"`
	DemolishedS   float64  `json:"demolished_s"`
}

type Hits struct {
	Count    int     `json:"count"`
	AvgSpeed float64 `json:"avg_speed"`
	MaxSpeed float64 `json:"max_speed"`
	// Points are my touches in order (older matches have none).
	Points []HitPoint `json:"points,omitempty"`
}

// HitPoint is one of my touches: when, where the ball was and its speed
// before and after (Match.SpeedUnit). Positions are seen from my team's side: my goal
// is at negative Y (the field spans X ±4096, Y ±5120, Z 0-2044 uu).
type HitPoint struct {
	T    float64 `json:"t"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Z    float64 `json:"z"`
	Pre  float64 `json:"pre"`
	Post float64 `json:"post"`
}

// Vec is a position in uu, seen from my team's side (my goal at negative Y).
type Vec struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// Demolition is a demolition I made (Mine) or took: Player is the other car.
type Demolition struct {
	T      float64 `json:"t"`
	Player string  `json:"player"`
	Mine   bool    `json:"mine"`
}

type Goal struct {
	T        float64 `json:"t"`
	Team     string  `json:"team"` // us | them | "" (unknown)
	Scorer   string  `json:"scorer"`
	Assister string  `json:"assister"`
	Speed    float64 `json:"speed"`
	MeScored bool    `json:"me_scored"`
	MeAssist bool    `json:"me_assist"`
	Overtime bool    `json:"overtime"`
	// Impact is where the ball crossed the goal line (older matches: nil).
	Impact *Vec `json:"impact,omitempty"`
}

// Normalize fills derived fields and guarantees non-nil collections so the
// JSON shape is stable for the frontend.
func (m *Match) Normalize() {
	if m.Players == nil {
		m.Players = []Player{}
	}
	if m.Goals == nil {
		m.Goals = []Goal{}
	}
	if m.Statfeed == nil {
		m.Statfeed = map[string]int{}
	}
	m.GoalDiff = m.TeamScore - m.OppScore
	if m.FirstGoal == "" {
		m.FirstGoal = "none"
		for _, g := range m.Goals {
			if g.Team == "us" || g.Team == "them" {
				m.FirstGoal = g.Team
				break
			}
		}
	}
	m.StartedAt = m.StartedAt.UTC().Truncate(time.Second)
	m.EndedAt = m.EndedAt.UTC().Truncate(time.Second)
}

// ProfileKey identifies the in-game account a match was played on (the
// player's "me"): its platform id, or its name when there is none. A player
// with several Rocket League accounts has one profile per account.
func ProfileKey(m *Match) string {
	if id := strings.ToLower(strings.TrimSpace(m.Me.PrimaryID)); id != "" {
		return id
	}
	if n := strings.ToLower(strings.TrimSpace(m.Me.Name)); n != "" {
		return "name:" + n
	}
	return ""
}

// SameProfile keeps the matches of m's profile (m's own history).
func SameProfile(ms []*Match, m *Match) []*Match {
	k := ProfileKey(m)
	out := make([]*Match, 0, len(ms))
	for _, x := range ms {
		if ProfileKey(x) == k {
			out = append(out, x)
		}
	}
	return out
}
