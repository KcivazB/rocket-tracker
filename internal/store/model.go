package store

import "time"

// Match is the JSON model served by GET /api/matches (see docs/SPEC.md §4).
type Match struct {
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
	SampleS       float64 `json:"sample_s"`
	AvgSpeed      float64 `json:"avg_speed"`
	SupersonicPct float64 `json:"supersonic_pct"`
	GroundPct     float64 `json:"ground_pct"`
	WallPct       float64 `json:"wall_pct"`
	AirPct        float64 `json:"air_pct"`
	AvgBoost      float64 `json:"avg_boost"`
	ZeroBoostPct  float64 `json:"zero_boost_pct"`
	FullBoostPct  float64 `json:"full_boost_pct"`
	BoostingPct   float64 `json:"boosting_pct"`
	PowerslidePct float64 `json:"powerslide_pct"`
	DemolishedS   float64 `json:"demolished_s"`
}

type Hits struct {
	Count    int     `json:"count"`
	AvgSpeed float64 `json:"avg_speed"`
	MaxSpeed float64 `json:"max_speed"`
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
