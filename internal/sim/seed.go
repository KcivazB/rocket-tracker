package sim

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"time"

	"rocket-tracker/internal/store"
)

// SeedOptions configures Generate.
type SeedOptions struct {
	N      int
	Now    time.Time
	Days   int // spread over the last Days days (default 90)
	Seed   uint64
	MeName string
	MeID   string
}

var mates = []struct {
	name    string
	id      string
	synergy float64
}{
	{"Kaiser", "Epic|mate-kaiser|0", 0.08},
	{"Nova", "Steam|76561198000000001|0", 0.03},
	{"Gizmo", "PS4|442211|0", -0.05},
	{"Lumen", "Epic|mate-lumen|0", 0.0},
	{"Raptor", "XboxOne|99812|0", -0.02},
}

func poisson(r *rand.Rand, lambda float64) int {
	l := math.Exp(-lambda)
	k, p := 0, 1.0
	for {
		p *= r.Float64()
		if p <= l {
			return k
		}
		k++
	}
}

func r1(f float64) float64 { return math.Round(f*10) / 10 }

// Generate builds N realistic finished matches spread over the last Days days,
// oldest first.
func Generate(o SeedOptions) []*store.Match {
	if o.N <= 0 {
		return nil
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	if o.Days <= 0 {
		o.Days = 90
	}
	if o.MeName == "" {
		o.MeName = "SimPlayer"
	}
	if o.MeID == "" {
		o.MeID = "Epic|sim-me-0001|0"
	}
	seed := o.Seed
	if seed == 0 {
		seed = uint64(time.Now().UnixNano())
	}
	r := rand.New(rand.NewPCG(seed, seed*31+7))

	// Build session start times.
	start := o.Now.Add(-time.Duration(o.Days) * 24 * time.Hour)
	var times []time.Time
	for len(times) < o.N {
		day := start.Add(time.Duration(r.IntN(o.Days)) * 24 * time.Hour)
		hour := 18 + r.IntN(6)
		if r.Float64() < 0.3 {
			hour = 10 + r.IntN(8)
		}
		t := time.Date(day.Year(), day.Month(), day.Day(), hour, r.IntN(60), 0, 0, time.Local)
		n := 2 + r.IntN(9)
		for i := 0; i < n && len(times) < o.N; i++ {
			if t.After(o.Now.Add(-10 * time.Minute)) {
				break
			}
			times = append(times, t)
			t = t.Add(time.Duration(6*60+r.IntN(5*60)) * time.Second)
		}
	}
	sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })

	out := make([]*store.Match, 0, o.N)
	lastResult := ""
	var prevEnd time.Time
	sessionIdx := 0
	for i, t := range times {
		// Sessions drawn on the same day may overlap, and long (overtime)
		// matches can outlast the planned gap: never start before the
		// previous match ended.
		if minStart := prevEnd.Add(time.Duration(60+r.IntN(120)) * time.Second); !prevEnd.IsZero() && t.Before(minStart) {
			t = minStart
		}
		if prevEnd.IsZero() || t.Sub(prevEnd) > 30*time.Minute {
			sessionIdx = 0
		}
		sessionIdx++
		progress := float64(i) / float64(len(times)) // slow improvement over time
		m := genMatch(r, o, t, progress, sessionIdx, lastResult)
		prevEnd = m.EndedAt
		lastResult = m.Result
		out = append(out, m)
	}
	// Pushing starts back may have moved the last matches into the future:
	// shift everything back uniformly (keeps the gaps, so no overlap).
	if n := len(out); n > 0 {
		if excess := out[n-1].EndedAt.Sub(o.Now.Add(-time.Minute)); excess > 0 {
			for _, m := range out {
				m.StartedAt, m.EndedAt = m.StartedAt.Add(-excess), m.EndedAt.Add(-excess)
			}
		}
	}
	return out
}

func genMatch(r *rand.Rand, o SeedOptions, t time.Time, progress float64, sessionIdx int, last string) *store.Match {
	size := 2
	switch x := r.Float64(); {
	case x < 0.2:
		size = 1
	case x < 0.65:
		size = 2
	default:
		size = 3
	}
	m := &store.Match{
		GUID:      fmt.Sprintf("%08X%08X%08X%08X", r.Uint32(), r.Uint32(), r.Uint32(), r.Uint32()),
		Online:    true,
		StartedAt: t,
		Arena:     arenas[r.IntN(len(arenas))],
		TeamSize:  size,
		Mode:      fmt.Sprintf("%dv%d", size, size),
		Variant:   "Soccar",
		Tag:       "ranked",
		MyTeam:    r.IntN(2),
	}
	if r.Float64() < 0.15 {
		m.Tag = "casual"
	} else if r.Float64() < 0.03 {
		m.Tag = "tournament"
	}
	if r.Float64() < 0.04 {
		m.Online = false
		m.GUID = fmt.Sprintf("local-%d", t.UnixMilli())
		m.Tag = "other"
	}

	// Players.
	type pl struct {
		store.Player
		skill float64
	}
	var ps []*pl
	me := &pl{Player: store.Player{Name: o.MeName, PrimaryID: o.MeID, Team: m.MyTeam, IsMe: true}, skill: 1.0 + 0.15*progress}
	ps = append(ps, me)
	synergy := 0.0
	used := map[string]bool{}
	for i := 1; i < size; i++ {
		if r.Float64() < 0.6 {
			mt := mates[r.IntN(len(mates))]
			if !used[mt.name] {
				used[mt.name] = true
				synergy += mt.synergy
				ps = append(ps, &pl{Player: store.Player{Name: mt.name, PrimaryID: mt.id, Team: m.MyTeam}, skill: 0.95 + r.Float64()*0.2})
				continue
			}
		}
		n := namePool[r.IntN(len(namePool))] + fmt.Sprint(r.IntN(99))
		ps = append(ps, &pl{Player: store.Player{Name: n, PrimaryID: fmt.Sprintf("%s|%d|0", platforms[r.IntN(len(platforms))], 1000000+r.IntN(9000000)), Team: m.MyTeam}, skill: 0.8 + r.Float64()*0.4})
	}
	for i := 0; i < size; i++ {
		n := namePool[r.IntN(len(namePool))] + fmt.Sprint(r.IntN(999))
		ps = append(ps, &pl{Player: store.Player{Name: n, PrimaryID: fmt.Sprintf("%s|%d|0", platforms[r.IntN(len(platforms))], 1000000+r.IntN(9000000)), Team: 1 - m.MyTeam}, skill: 0.85 + r.Float64()*0.4})
	}

	// Win probability: base + improvement + synergy - fatigue - tilt.
	pWin := 0.48 + 0.08*progress + synergy - 0.012*float64(max(0, sessionIdx-4))
	if last == "loss" {
		pWin -= 0.04
	}
	hour := t.Hour()
	if hour >= 23 || hour < 2 {
		pWin -= 0.05
	}
	win := r.Float64() < pWin

	gUs := poisson(r, 2.4)
	gThem := poisson(r, 2.4)
	if win && gUs <= gThem {
		gUs, gThem = gThem, gUs
	} else if !win && gUs >= gThem {
		gUs, gThem = gThem, gUs
	}
	overtime := false
	if gUs == gThem || (r.Float64() < 0.08 && abs(gUs-gThem) <= 1) {
		// Tie at the end of regulation → overtime decides.
		overtime = true
		if win {
			gThem = gUs
			gUs++
		} else {
			gUs = gThem
			gThem++
		}
	}
	m.Result = map[bool]string{true: "win", false: "loss"}[win]
	duration := 300.0
	if overtime {
		m.Overtime = true
		m.OvertimeS = float64(5 + r.IntN(150))
		duration += m.OvertimeS
	}
	forfeitRoll := r.Float64()
	if !overtime && forfeitRoll < 0.05 && abs(gUs-gThem) >= 2 {
		m.Forfeit = true
		duration = float64(90 + r.IntN(180))
	} else if forfeitRoll > 0.98 {
		m.Result = "abandoned"
		duration = float64(40 + r.IntN(200))
	}
	m.DurationS = duration
	m.TeamScore, m.OppScore = gUs, gThem
	m.EndedAt = t.Add(time.Duration(duration*1.15+30) * time.Second)
	if m.Result == "abandoned" {
		m.EndedAt = t.Add(time.Duration(duration+20) * time.Second)
	}

	// Goals timeline.
	var goals []store.Goal
	regDur := math.Min(duration, 300)
	for i := 0; i < gUs; i++ {
		goals = append(goals, store.Goal{Team: "us", T: r1(r.Float64() * regDur)})
	}
	for i := 0; i < gThem; i++ {
		goals = append(goals, store.Goal{Team: "them", T: r1(r.Float64() * regDur)})
	}
	sort.Slice(goals, func(i, j int) bool { return goals[i].T < goals[j].T })
	if overtime && len(goals) > 0 {
		// The last goal is the overtime winner.
		w := "them"
		if win {
			w = "us"
		}
		for i := len(goals) - 1; i >= 0; i-- {
			if goals[i].Team == w {
				g := goals[i]
				goals = append(goals[:i], goals[i+1:]...)
				g.T = r1(300 + m.OvertimeS)
				g.Overtime = true
				goals = append(goals, g)
				break
			}
		}
	}
	// Assign scorers / assisters.
	pick := func(team int) *pl {
		var c []*pl
		tot := 0.0
		for _, p := range ps {
			if p.Team == team {
				c = append(c, p)
				tot += p.skill
			}
		}
		x := r.Float64() * tot
		for _, p := range c {
			x -= p.skill
			if x <= 0 {
				return p
			}
		}
		return c[len(c)-1]
	}
	statfeed := map[string]int{}
	for i := range goals {
		g := &goals[i]
		team := m.MyTeam
		if g.Team == "them" {
			team = 1 - m.MyTeam
		}
		sc := pick(team)
		sc.Goals++
		sc.Score += 100
		g.Scorer = sc.Name
		g.Speed = r1(1500 + r.Float64()*3200)
		g.MeScored = sc.IsMe
		if size > 1 && r.Float64() < 0.5 {
			if a := pick(team); a != sc {
				a.Assists++
				a.Score += 50
				g.Assister = a.Name
				g.MeAssist = a.IsMe
			}
		}
		if sc.IsMe {
			statfeed["Goal"]++
			if r.Float64() < 0.2 {
				statfeed["AerialGoal"]++
			}
			if g.Speed > 4200 {
				statfeed["LongGoal"]++
			}
			if g.Overtime {
				statfeed["OvertimeGoal"]++
			}
		}
		if g.MeAssist {
			statfeed["Assist"]++
		}
	}
	scale := duration / 300
	for _, p := range ps {
		p.Shots += p.Goals + poisson(r, 1.6*p.skill*scale)
		p.Saves += poisson(r, 1.5*scale)
		p.Demos += poisson(r, 0.6*scale)
		p.Touches = 10 + poisson(r, 22*p.skill*scale)
		p.Score += p.Shots*20 + p.Saves*50 + p.Demos*10 + p.Touches*2 + r.IntN(40)
	}
	statfeed["Shot"] += me.Shots
	if me.Saves > 0 {
		epic := r.IntN(me.Saves+1) / 2
		statfeed["Save"] += me.Saves - epic
		if epic > 0 {
			statfeed["EpicSave"] += epic
		}
	}
	if me.Demos > 0 {
		statfeed["Demolish"] = me.Demos
	}
	if me.Goals >= 3 {
		statfeed["HatTrick"] = 1
	}
	if win && m.Result == "win" {
		statfeed["Win"] = 1
		best := true
		for _, p := range ps {
			if p.Team == m.MyTeam && p.Score > me.Score {
				best = false
			}
		}
		if best {
			statfeed["MVP"] = 1
			m.MVP = true
		}
	}
	if m.Result == "abandoned" {
		statfeed = map[string]int{}
		m.MVP = false
		for _, p := range ps {
			p.Score /= 2
		}
	}
	m.Statfeed = statfeed
	m.Goals = goals // abandoned matches keep the goals scored before leaving
	for _, p := range ps {
		m.Players = append(m.Players, p.Player)
	}
	m.Me = store.MeStats{Name: me.Name, PrimaryID: me.PrimaryID, Score: me.Score, Goals: me.Goals, Shots: me.Shots,
		Assists: me.Assists, Saves: me.Saves, Touches: me.Touches, Demos: me.Demos}

	// Movement & hits (sometimes missing, like when the API had no spectator data).
	if r.Float64() < 0.95 {
		ground := 55 + r.NormFloat64()*5
		wall := 8 + r.NormFloat64()*2
		m.Movement = &store.Movement{
			SampleS:       r1(duration * (0.85 + r.Float64()*0.1)),
			AvgSpeed:      r1(1300 + 150*progress + r.NormFloat64()*90),
			SupersonicPct: r1(math.Max(0, 14+5*progress+r.NormFloat64()*3)),
			GroundPct:     r1(ground),
			WallPct:       r1(wall),
			AirPct:        r1(100 - ground - wall),
			AvgBoost:      r1(38 + r.NormFloat64()*6),
			ZeroBoostPct:  r1(math.Max(0, 9+r.NormFloat64()*3)),
			FullBoostPct:  r1(math.Max(0, 7+r.NormFloat64()*2)),
			BoostingPct:   r1(math.Max(0, 17+r.NormFloat64()*3)),
			PowerslidePct: r1(math.Max(0, 3+r.NormFloat64())),
			DemolishedS:   r1(float64(poisson(r, 1.2)) * 3),
		}
	}
	if me.Touches > 0 {
		m.Hits = &store.Hits{Count: me.Touches, AvgSpeed: r1(1900 + r.NormFloat64()*200), MaxSpeed: r1(3500 + r.Float64()*1500)}
	}
	if m.Variant == "Soccar" {
		switch m.Arena {
		case "HoopsStadium_P":
			m.Variant = "Hoops"
		case "ShatterShot_P":
			m.Variant = "Dropshot"
		}
	}
	m.Normalize()
	return m
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
