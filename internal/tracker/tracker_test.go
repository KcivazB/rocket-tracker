package tracker

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"rocket-tracker/internal/statsapi"
	"rocket-tracker/internal/store"
)

// ---- harness ----

type harness struct {
	t     *testing.T
	now   time.Time
	tr    *Tracker
	saved []*store.Match
	id    Identity
	auto  [][2]string
}

func newHarness(t *testing.T) *harness {
	h := &harness{t: t, now: time.Date(2026, 10, 8, 20, 0, 0, 0, time.UTC)}
	h.tr = New(Options{
		Now:            func() time.Time { return h.now },
		Identity:       func() Identity { return h.id },
		OnAutoIdentity: func(n, id string) { h.auto = append(h.auto, [2]string{n, id}) },
		Save: func(m *store.Match) error {
			m.ID = int64(len(h.saved) + 1)
			h.saved = append(h.saved, m)
			return nil
		},
	})
	return h
}

func (h *harness) advance(d time.Duration) { h.now = h.now.Add(d) }

func (h *harness) send(name string, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		h.t.Fatal(err)
	}
	h.tr.HandleEvent(statsapi.Event{Name: name, Data: b})
}

type P struct {
	Name, ID       string
	Short, Team    int
	Score, Goals   int
	Shots, Assists int
	Saves, Touches int
	Demos          int
	Spec           map[string]any // spectator fields (nil => absent)
}

func (p P) ref() map[string]any {
	return map[string]any{"Name": p.Name, "Shortcut": p.Short, "TeamNum": p.Team}
}

type G struct {
	Guid     string
	Players  []P
	Time     int
	OT       bool
	Replay   bool
	Scores   [2]int
	Arena    string
	Target   *P
	NoTarget bool
}

func (h *harness) update(g G) {
	var ps []map[string]any
	for _, p := range g.Players {
		m := map[string]any{"Name": p.Name, "PrimaryId": p.ID, "Shortcut": p.Short, "TeamNum": p.Team, "Score": p.Score,
			"Goals": p.Goals, "Shots": p.Shots, "Assists": p.Assists, "Saves": p.Saves, "Touches": p.Touches, "Demos": p.Demos}
		for k, v := range p.Spec {
			m[k] = v
		}
		ps = append(ps, m)
	}
	arena := g.Arena
	if arena == "" {
		arena = "Stadium_P"
	}
	game := map[string]any{
		"Teams":       []map[string]any{{"TeamNum": 0, "Score": g.Scores[0]}, {"TeamNum": 1, "Score": g.Scores[1]}},
		"TimeSeconds": g.Time, "bOvertime": g.OT, "bReplay": g.Replay, "Arena": arena,
		"bHasTarget": g.Target != nil && !g.NoTarget,
	}
	if g.Target != nil {
		game["Target"] = g.Target.ref()
	}
	h.send("UpdateState", map[string]any{"MatchGuid": g.Guid, "Players": ps, "Game": game})
}

func (h *harness) only() *store.Match {
	h.t.Helper()
	if len(h.saved) != 1 {
		h.t.Fatalf("expected 1 saved match, got %d", len(h.saved))
	}
	return h.saved[0]
}

var (
	me  = P{Name: "Me", ID: "Epic|me|0", Short: 1, Team: 0}
	mt  = P{Name: "Mate", ID: "Steam|mate|0", Short: 2, Team: 0}
	op1 = P{Name: "Opp1", ID: "PS4|o1|0", Short: 3, Team: 1}
	op2 = P{Name: "Opp2", ID: "XboxOne|o2|0", Short: 4, Team: 1}
)

// playRegulation sends one update per real 100ms, ticking the clock down
// from `from` to `to` (one game second per 4 updates).
func (h *harness) playClock(g G, from, to int) G {
	g.Time = from
	h.update(g)
	for ts := from; ts >= to; ts-- {
		g.Time = ts
		for i := 0; i < 4; i++ {
			h.advance(250 * time.Millisecond)
			h.update(g)
		}
	}
	return g
}

// ---- tests ----

func TestWinRegulation(t *testing.T) {
	h := newHarness(t)
	h.id = Identity{Names: []string{"me"}} // case-insensitive name match
	g := G{Guid: "GUID1", Players: []P{me, mt, op1, op2}}
	h.send("MatchCreated", map[string]any{"MatchGuid": "GUID1"})
	h.send("MatchInitialized", map[string]any{"MatchGuid": "GUID1"})
	h.send("CountdownBegin", map[string]any{"MatchGuid": "GUID1"})
	g = h.playClock(g, 300, 300)
	h.send("RoundStarted", map[string]any{"MatchGuid": "GUID1"})
	g = h.playClock(g, 299, 250) // 50 s elapsed

	// Goal by me, assisted by mate.
	h.send("GoalScored", map[string]any{"MatchGuid": "GUID1", "GoalSpeed": 3000.5, "Scorer": me.ref(), "Assister": mt.ref()})
	g.Scores = [2]int{1, 0}
	g.Players[0].Goals, g.Players[0].Score = 1, 300
	g.Players[1].Assists, g.Players[1].Score = 1, 150
	h.send("StatfeedEvent", map[string]any{"MatchGuid": "GUID1", "EventName": "Goal", "MainTarget": me.ref()})
	h.send("StatfeedEvent", map[string]any{"MatchGuid": "GUID1", "EventName": "Assist", "MainTarget": mt.ref()})
	h.send("CountdownBegin", map[string]any{})
	h.send("RoundStarted", map[string]any{})
	g = h.playClock(g, 249, 100)
	// Opponent goal.
	h.send("GoalScored", map[string]any{"MatchGuid": "GUID1", "GoalSpeed": "2000", "Scorer": op1.ref()})
	g.Scores = [2]int{1, 1}
	h.send("RoundStarted", map[string]any{})
	g = h.playClock(g, 99, 30)
	h.send("GoalScored", map[string]any{"MatchGuid": "GUID1", "GoalSpeed": 1000, "Scorer": mt.ref()})
	g.Scores = [2]int{2, 1}
	g.Players[1].Goals, g.Players[1].Score = 1, 400
	h.send("RoundStarted", map[string]any{})
	g = h.playClock(g, 29, 0)
	h.send("MatchEnded", map[string]any{"MatchGuid": "GUID1", "WinnerTeamNum": 0})
	h.send("PodiumStart", map[string]any{})
	h.update(g) // podium update must not create a new match
	h.send("MatchDestroyed", map[string]any{"MatchGuid": "GUID1"})

	m := h.only()
	if m.Result != "win" || m.Forfeit || m.Overtime {
		t.Fatalf("result=%s forfeit=%v ot=%v", m.Result, m.Forfeit, m.Overtime)
	}
	if m.TeamScore != 2 || m.OppScore != 1 || m.GoalDiff != 1 {
		t.Fatalf("score %d-%d diff %d", m.TeamScore, m.OppScore, m.GoalDiff)
	}
	if m.Mode != "2v2" || m.TeamSize != 2 || m.Variant != "Soccar" || !m.Online || m.GUID != "GUID1" {
		t.Fatalf("mode=%s size=%d variant=%s online=%v guid=%s", m.Mode, m.TeamSize, m.Variant, m.Online, m.GUID)
	}
	if m.DurationS != 300 {
		t.Fatalf("duration %v", m.DurationS)
	}
	if m.FirstGoal != "us" || len(m.Goals) != 3 {
		t.Fatalf("first goal %s, goals %d", m.FirstGoal, len(m.Goals))
	}
	g0 := m.Goals[0]
	if g0.Team != "us" || !g0.MeScored || g0.MeAssist || g0.Assister != "Mate" || g0.T != 50 || g0.Speed != 3000.5 {
		t.Fatalf("goal0 %+v", g0)
	}
	if m.Goals[1].Team != "them" || m.Goals[1].MeScored || m.Goals[1].Speed != 2000 {
		t.Fatalf("goal1 %+v", m.Goals[1])
	}
	if m.Statfeed["Goal"] != 1 || m.Statfeed["Assist"] != 0 {
		t.Fatalf("statfeed %v", m.Statfeed)
	}
	// Mate has the higher score => not MVP.
	if m.MVP {
		t.Fatal("unexpected MVP")
	}
	if m.Me.Name != "Me" || m.Me.Goals != 1 || len(m.Players) != 4 {
		t.Fatalf("me %+v players %d", m.Me, len(m.Players))
	}
	if m.Tag != "ranked" {
		t.Fatalf("tag %s", m.Tag)
	}
	if len(h.auto) != 0 {
		t.Fatal("identity from config must not trigger auto persistence")
	}
}

func TestLossByIDAndMVPStatfeed(t *testing.T) {
	h := newHarness(t)
	h.id = Identity{IDs: []string{"epic|ME|0"}, Names: []string{"Mate"}} // id wins over name
	g := G{Guid: "G2", Players: []P{me, mt, op1, op2}}
	g = h.playClock(g, 300, 0)
	h.send("StatfeedEvent", map[string]any{"EventName": "MVP", "MainTarget": me.ref()})
	h.send("MatchEnded", map[string]any{"WinnerTeamNum": 1})
	m := h.only()
	if m.Result != "loss" || m.Me.Name != "Me" || m.MyTeam != 0 {
		t.Fatalf("%s %s", m.Result, m.Me.Name)
	}
	if !m.MVP {
		t.Fatal("MVP from statfeed expected")
	}
}

func TestOvertime(t *testing.T) {
	h := newHarness(t)
	h.id = Identity{Names: []string{"Me"}}
	g := G{Guid: "OT", Players: []P{me, op1}}
	g.Players[0].Score = 500
	g = h.playClock(g, 300, 0)
	g.OT = true
	g.Time = 0
	h.update(g)
	for ts := 1; ts <= 42; ts++ {
		h.advance(time.Second)
		h.send("ClockUpdatedSeconds", map[string]any{"TimeSeconds": ts, "bOvertime": true})
	}
	h.send("GoalScored", map[string]any{"Scorer": me.ref(), "GoalSpeed": 1234})
	// No UpdateState after the OT winner: score must come from GoalScored.
	h.send("MatchEnded", map[string]any{"WinnerTeamNum": 0})
	if m := h.only(); m.TeamScore != 1 || m.OppScore != 0 || m.Me.Goals != 1 {
		t.Fatalf("score %d-%d me goals %d", m.TeamScore, m.OppScore, m.Me.Goals)
	}
	m := h.only()
	if !m.Overtime || m.OvertimeS != 42 || m.DurationS != 342 || m.Forfeit || m.Result != "win" {
		t.Fatalf("ot=%v ots=%v dur=%v forfeit=%v res=%s", m.Overtime, m.OvertimeS, m.DurationS, m.Forfeit, m.Result)
	}
	if m.Mode != "1v1" {
		t.Fatalf("mode %s", m.Mode)
	}
	if len(m.Goals) != 1 || !m.Goals[0].Overtime || m.Goals[0].T != 342 {
		t.Fatalf("goals %+v", m.Goals)
	}
	if !m.MVP { // won and best score of my team
		t.Fatal("computed MVP expected")
	}
}

func TestForfeit(t *testing.T) {
	h := newHarness(t)
	h.id = Identity{Names: []string{"Me"}}
	g := G{Guid: "FF", Players: []P{me, op1}, Scores: [2]int{3, 0}}
	g = h.playClock(g, 300, 120)
	h.send("MatchEnded", map[string]any{"WinnerTeamNum": "0"}) // string number tolerated
	m := h.only()
	if !m.Forfeit || m.Result != "win" || m.DurationS != 180 {
		t.Fatalf("forfeit=%v res=%s dur=%v", m.Forfeit, m.Result, m.DurationS)
	}
}

func TestAbandonedSavedAndDropped(t *testing.T) {
	// Long enough: saved as abandoned.
	h := newHarness(t)
	h.id = Identity{Names: []string{"Me"}}
	g := G{Guid: "AB", Players: []P{me, mt, op1, op2}}
	h.playClock(g, 300, 200)
	h.send("MatchDestroyed", map[string]any{"MatchGuid": "AB"})
	m := h.only()
	if m.Result != "abandoned" || m.Forfeit || m.MVP {
		t.Fatalf("res=%s forfeit=%v", m.Result, m.Forfeit)
	}

	// Too short: dropped.
	h2 := newHarness(t)
	h2.id = h.id
	h2.playClock(G{Guid: "AB2", Players: []P{me, op1}}, 300, 290)
	h2.send("MatchDestroyed", map[string]any{})
	if len(h2.saved) != 0 {
		t.Fatal("short abandoned match must be dropped")
	}

	// Game closed mid-match (disconnect) => abandoned.
	h3 := newHarness(t)
	h3.id = h.id
	h3.playClock(G{Guid: "AB3", Players: []P{me, op1}}, 300, 150)
	h3.tr.Disconnected()
	if len(h3.saved) != 1 || h3.saved[0].Result != "abandoned" {
		t.Fatalf("disconnect: %d saved", len(h3.saved))
	}

	// New match guid while one is running => previous abandoned, new one tracked.
	h4 := newHarness(t)
	h4.id = h.id
	h4.playClock(G{Guid: "A", Players: []P{me, op1}}, 300, 200)
	h4.playClock(G{Guid: "B", Players: []P{me, op1}}, 300, 299)
	if len(h4.saved) != 1 || h4.saved[0].GUID != "A" || h4.saved[0].Result != "abandoned" {
		t.Fatalf("new match: %+v", h4.saved)
	}
	if l := h4.tr.Live(); l == nil || l.GUID != "B" {
		t.Fatalf("live %+v", l)
	}
}

func TestAutoIdentityByTarget(t *testing.T) {
	h := newHarness(t)
	g := G{Guid: "AUTO", Players: []P{me, mt, op1, op2}, Target: &me}
	g = h.playClock(g, 300, 0)
	// A few frames targeting the mate (e.g. spectating after a demo) and
	// replay frames targeting the opponent must not win.
	g.Target = &mt
	for i := 0; i < 5; i++ {
		h.update(g)
	}
	g.Target, g.Replay = &op1, true
	for i := 0; i < 2000; i++ {
		h.update(g)
	}
	h.send("MatchEnded", map[string]any{"WinnerTeamNum": 0})
	m := h.only()
	if m.Me.Name != "Me" || m.Me.PrimaryID != "Epic|me|0" {
		t.Fatalf("me=%+v", m.Me)
	}
	if len(h.auto) != 1 || h.auto[0] != [2]string{"Me", "Epic|me|0"} {
		t.Fatalf("auto identity callback %v", h.auto)
	}
}

func TestSkipRules(t *testing.T) {
	// Spectating: nobody identified (no config, no target).
	h := newHarness(t)
	h.playClock(G{Guid: "S", Players: []P{op1, op2}}, 300, 0)
	h.send("MatchEnded", map[string]any{"WinnerTeamNum": 0})
	if len(h.saved) != 0 {
		t.Fatal("spectated match must be skipped")
	}
	// Offline freeplay with one player.
	h2 := newHarness(t)
	h2.id = Identity{Names: []string{"Me"}}
	h2.playClock(G{Guid: "", Players: []P{me}}, 300, 0)
	h2.send("MatchEnded", map[string]any{"WinnerTeamNum": 0})
	if len(h2.saved) != 0 {
		t.Fatal("freeplay must be skipped")
	}
	// Offline vs bots: saved with a local guid.
	h3 := newHarness(t)
	h3.id = Identity{Names: []string{"Me"}}
	h3.send("MatchCreated", map[string]any{"MatchGuid": ""})
	h3.send("MatchInitialized", map[string]any{"MatchGuid": ""})
	h3.playClock(G{Guid: "", Players: []P{me, {Name: "Bot", ID: "Unknown|0|0", Short: 2, Team: 1}}, Arena: "HoopsStadium_P"}, 300, 0)
	h3.send("MatchEnded", map[string]any{"WinnerTeamNum": 1})
	m := h3.only()
	if m.Online || m.GUID[:6] != "local-" || m.Result != "loss" || m.Variant != "Hoops" {
		t.Fatalf("offline %+v", m)
	}
	// No players at all.
	h4 := newHarness(t)
	h4.send("MatchCreated", map[string]any{"MatchGuid": "X"})
	h4.send("MatchEnded", map[string]any{"WinnerTeamNum": 1})
	if len(h4.saved) != 0 {
		t.Fatal("no players must be skipped")
	}
}

func TestMovementAccumulation(t *testing.T) {
	h := newHarness(t)
	h.id = Identity{Names: []string{"Me"}}
	ground := map[string]any{"bHasCar": true, "Speed": 1000, "Boost": 100, "bBoosting": false, "bOnGround": true,
		"bOnWall": false, "bPowersliding": false, "bDemolished": false, "bSupersonic": false}
	air := map[string]any{"bHasCar": true, "Speed": 2300, "Boost": 0, "bBoosting": true, "bOnGround": false,
		"bOnWall": false, "bPowersliding": false, "bDemolished": false, "bSupersonic": true}
	demo := map[string]any{"bHasCar": false, "Speed": 0, "Boost": 33, "bOnGround": false, "bDemolished": true}

	p := me
	g := G{Guid: "MV", Players: []P{p, op1}}
	h.send("MatchCreated", map[string]any{"MatchGuid": "MV"})
	h.send("CountdownBegin", map[string]any{})
	g.Time = 300
	// During countdown: clock not running => no samples.
	for i := 0; i < 10; i++ {
		g.Players[0].Spec = ground
		h.advance(250 * time.Millisecond)
		h.update(g)
	}
	h.send("RoundStarted", map[string]any{})
	// 10 game seconds: 5 on ground, 5 in the air, 4 samples per second.
	for s := 0; s < 10; s++ {
		g.Time = 299 - s
		if s < 5 {
			g.Players[0].Spec = ground
		} else {
			g.Players[0].Spec = air
		}
		for i := 0; i < 4; i++ {
			h.advance(250 * time.Millisecond)
			h.update(g)
		}
	}
	// 2 seconds demolished.
	for s := 0; s < 2; s++ {
		g.Time--
		g.Players[0].Spec = demo
		for i := 0; i < 4; i++ {
			h.advance(250 * time.Millisecond)
			h.update(g)
		}
	}
	// Replay frames are ignored even with a running clock.
	g.Replay = true
	g.Players[0].Spec = air
	for i := 0; i < 20; i++ {
		g.Time--
		h.advance(250 * time.Millisecond)
		h.update(g)
	}
	g.Replay = false
	// Pause: ignored.
	h.send("MatchPaused", map[string]any{})
	for i := 0; i < 20; i++ {
		g.Time--
		h.advance(250 * time.Millisecond)
		h.update(g)
	}
	h.send("MatchUnpaused", map[string]any{})
	// A big real-time gap is capped at 0.5 s.
	g.Players[0].Spec = ground
	g.Time--
	h.advance(10 * time.Second)
	h.update(g)
	// Freeze: same TimeSeconds for > 1.5 s => not counted.
	for i := 0; i < 12; i++ {
		h.advance(250 * time.Millisecond)
		h.update(g)
	}

	// Opponent never has spectator fields: no movement for them, but that's
	// irrelevant; end the match.
	h.send("MatchEnded", map[string]any{"WinnerTeamNum": 0})
	m := h.only()
	mv := m.Movement
	if mv == nil {
		t.Fatal("movement missing")
	}
	// Expected: 40 samples * 0.25 s car time (but the first sample after
	// RoundStarted counts too) => car = 10 s ground/air + 0.5 s capped gap +
	// up to 1.5 s of not-yet-frozen samples; demolished = 2 s.
	approx := func(name string, got, want, tol float64) {
		t.Helper()
		if math.Abs(got-want) > tol {
			t.Errorf("%s = %v, want %v ± %v", name, got, want, tol)
		}
	}
	approx("demolished_s", mv.DemolishedS, 2, 0.3)
	carTime := mv.SampleS - mv.DemolishedS
	approx("car time", carTime, 12, 1.0)
	approx("air+ground+wall", mv.AirPct+mv.GroundPct+mv.WallPct, 100, 0.5)
	airShare := 5.0 / carTime * 100
	approx("air_pct", mv.AirPct, airShare, 2)
	approx("supersonic_pct", mv.SupersonicPct, airShare, 2)
	approx("boosting_pct", mv.BoostingPct, airShare, 2)
	approx("zero_boost_pct", mv.ZeroBoostPct, airShare, 2)
	approx("full_boost_pct", mv.FullBoostPct, 100-airShare, 2)
	wantSpeed := (5*2300 + (carTime-5)*1000) / carTime
	approx("avg_speed", mv.AvgSpeed, wantSpeed, 40)
}

func TestBallHitsAndStatfeedForMeOnly(t *testing.T) {
	h := newHarness(t)
	h.id = Identity{Names: []string{"Me"}}
	g := G{Guid: "H", Players: []P{me, op1}}
	g = h.playClock(g, 300, 250)
	h.send("BallHit", map[string]any{"Players": []any{me.ref()}, "Ball": map[string]any{"PreHitSpeed": 100, "PostHitSpeed": 2000}})
	h.send("BallHit", map[string]any{"Players": []any{me.ref()}, "Ball": map[string]any{"PostHitSpeed": 4000}})
	h.send("BallHit", map[string]any{"Players": []any{op1.ref()}, "Ball": map[string]any{"PostHitSpeed": 9000}})
	h.send("BallHit", map[string]any{"Players": me.ref(), "Ball": map[string]any{"PostHitSpeed": 3000}}) // single object
	h.send("StatfeedEvent", map[string]any{"EventName": "Demolish", "MainTarget": me.ref(), "SecondaryTarget": op1.ref()})
	h.send("StatfeedEvent", map[string]any{"EventName": "Demolish", "MainTarget": op1.ref(), "SecondaryTarget": me.ref()})
	h.send("StatfeedEvent", map[string]any{"EventName": "EpicSave", "MainTarget": me.ref()})
	g = h.playClock(g, 249, 0)
	h.send("MatchEnded", map[string]any{"WinnerTeamNum": 0})
	m := h.only()
	if m.Hits == nil || m.Hits.Count != 3 || m.Hits.MaxSpeed != 4000 || m.Hits.AvgSpeed != 3000 {
		t.Fatalf("hits %+v", m.Hits)
	}
	if m.Statfeed["Demolish"] != 1 || m.Statfeed["EpicSave"] != 1 {
		t.Fatalf("statfeed %v", m.Statfeed)
	}
	if m.Movement != nil {
		t.Fatalf("no spectator fields => movement must be nil, got %+v", m.Movement)
	}
}

func TestGoalsUsThemFromOrangeSide(t *testing.T) {
	h := newHarness(t)
	meO := P{Name: "Me", ID: "Epic|me|0", Short: 2, Team: 1}
	blue := P{Name: "Blue", ID: "Steam|b|0", Short: 1, Team: 0}
	h.id = Identity{IDs: []string{"Epic|me|0"}}
	g := G{Guid: "OR", Players: []P{blue, meO}}
	g = h.playClock(g, 300, 280)
	h.send("GoalScored", map[string]any{"Scorer": blue.ref()})
	g = h.playClock(g, 279, 200)
	h.send("GoalScored", map[string]any{"Scorer": meO.ref()})
	h.send("GoalScored", map[string]any{"Scorer": meO.ref()})
	g.Scores = [2]int{1, 2}
	g = h.playClock(g, 199, 0)
	h.send("MatchEnded", map[string]any{"WinnerTeamNum": 1})
	m := h.only()
	if m.MyTeam != 1 || m.TeamScore != 2 || m.OppScore != 1 || m.Result != "win" || m.FirstGoal != "them" {
		t.Fatalf("%+v", m)
	}
	if m.Goals[0].Team != "them" || m.Goals[1].Team != "us" || !m.Goals[1].MeScored || m.Goals[0].T != 20 {
		t.Fatalf("goals %+v", m.Goals)
	}
}

func TestNeverPanicsOnGarbage(t *testing.T) {
	h := newHarness(t)
	inputs := []statsapi.Event{
		{Name: "UpdateState", Data: json.RawMessage(`{"Players": "nope", "Game": 5}`)},
		{Name: "UpdateState", Data: json.RawMessage(`{"Players": [null, 1, {"Name": 7, "TeamNum": "x"}], "Game": {"Teams": {}, "TimeSeconds": "abc", "Target": "x"}}`)},
		{Name: "GoalScored", Data: json.RawMessage(`{"Scorer": [], "GoalSpeed": {}}`)},
		{Name: "BallHit", Data: json.RawMessage(`{"Players": [1,2,3], "Ball": "fast"}`)},
		{Name: "StatfeedEvent", Data: json.RawMessage(`{"MainTarget": null}`)},
		{Name: "MatchEnded", Data: json.RawMessage(`{"WinnerTeamNum": null}`)},
		{Name: "ClockUpdatedSeconds", Data: json.RawMessage(`not json`)},
		{Name: "Unknown", Data: nil},
		{Name: "MatchDestroyed", Data: json.RawMessage(`[]`)},
	}
	for _, ev := range inputs {
		h.tr.HandleEvent(ev)
	}
	h.tr.Disconnected()
	_ = h.tr.Live()
}

func TestOfflineGuidAdoptedAndLive(t *testing.T) {
	h := newHarness(t)
	h.id = Identity{Names: []string{"Me"}}
	h.send("MatchCreated", map[string]any{"MatchGuid": ""})
	h.update(G{Guid: "LATE", Players: []P{me, op1}, Time: 300, Scores: [2]int{0, 1}})
	l := h.tr.Live()
	if l == nil || l.GUID != "LATE" || l.Me == nil || l.Me.Name != "Me" || l.OppScore != 1 || l.Mode != "1v1" {
		t.Fatalf("live %+v", l)
	}
	if len(l.Players) != 1 || l.Players[0].Name != op1.Name || l.Players[0].PrimaryID != op1.ID || l.Players[0].Team != 1 {
		t.Fatalf("live players %+v", l.Players)
	}
}

func TestReplayViewingSkipped(t *testing.T) {
	h := newHarness(t)
	h.id = Identity{Names: []string{"Me"}}
	g := G{Guid: "REPLAY", Players: []P{me, op1}, Replay: true}
	h.playClock(g, 300, 0)
	h.send("MatchEnded", map[string]any{"WinnerTeamNum": 0})
	if len(h.saved) != 0 {
		t.Fatal("a match made only of replay frames must not be saved")
	}
}

// Regression: with "play again" the next match can be created before the
// previous one is destroyed; the late MatchDestroyed used to abandon the new
// match.
func TestLateMatchDestroyedForPreviousMatch(t *testing.T) {
	h := newHarness(t)
	h.id = Identity{Names: []string{"Me"}}
	g := h.playClock(G{Guid: "A", Players: []P{me, op1}, Scores: [2]int{1, 0}}, 300, 0)
	h.send("MatchEnded", map[string]any{"MatchGuid": "A", "WinnerTeamNum": 0})
	h.update(g)
	h.send("MatchCreated", map[string]any{"MatchGuid": "B"})
	createdB := h.now
	gb := h.playClock(G{Guid: "B", Players: []P{me, op1}}, 300, 290)
	h.send("StatfeedEvent", map[string]any{"MatchGuid": "B", "EventName": "Shot", "MainTarget": me.ref()})
	h.send("MatchDestroyed", map[string]any{"MatchGuid": "A"})
	h.playClock(gb, 289, 0)
	h.send("MatchEnded", map[string]any{"MatchGuid": "B", "WinnerTeamNum": 1})
	if len(h.saved) != 2 || h.saved[0].Result != "win" || h.saved[1].GUID != "B" || h.saved[1].Result != "loss" {
		t.Fatalf("saved %+v", h.saved)
	}
	if b := h.saved[1]; !b.StartedAt.Equal(createdB) || b.Statfeed["Shot"] != 1 {
		t.Fatalf("match B lost its beginning: started %v statfeed %v", b.StartedAt, b.Statfeed)
	}

	// Same when the next match has no guid (offline / not known yet).
	h2 := newHarness(t)
	h2.id = h.id
	h2.playClock(G{Guid: "C", Players: []P{me, op1}}, 300, 200)
	h2.send("MatchEnded", map[string]any{"MatchGuid": "C", "WinnerTeamNum": 0})
	h2.send("MatchCreated", map[string]any{"MatchGuid": ""})
	h2.send("MatchDestroyed", map[string]any{"MatchGuid": "C"})
	if !h2.tr.InMatch() {
		t.Fatal("offline match killed by the previous match's MatchDestroyed")
	}
}

// Regression: an UpdateState of an ended match arriving after its
// MatchDestroyed restarted it, and it was later saved again as "abandoned"
// (overwriting the real result through the guid upsert).
func TestEndedMatchNotRestartedByTrailingUpdate(t *testing.T) {
	h := newHarness(t)
	h.id = Identity{Names: []string{"Me"}}
	g := h.playClock(G{Guid: "E", Players: []P{me, op1}, Scores: [2]int{2, 0}}, 300, 0)
	h.send("MatchEnded", map[string]any{"MatchGuid": "E", "WinnerTeamNum": 0})
	h.send("MatchDestroyed", map[string]any{"MatchGuid": "E"})
	h.update(g)
	h.send("MatchInitialized", map[string]any{"MatchGuid": "E"})
	if h.tr.InMatch() {
		t.Fatal("ended match restarted")
	}
	h.playClock(G{Guid: "F", Players: []P{me, op1}}, 300, 290)
	h.tr.Disconnected()
	if len(h.saved) != 1 || h.saved[0].GUID != "E" || h.saved[0].Result != "win" {
		t.Fatalf("saved %+v", h.saved)
	}
}

// Regression: reconnecting to the same match after a game crash started a
// fresh match: goals/statfeed/hits/movement of the first part were lost and
// started_at moved.
func TestReconnectSameGuidResumes(t *testing.T) {
	h := newHarness(t)
	h.id = Identity{Names: []string{"Me"}}
	start := h.now
	g := G{Guid: "R", Players: []P{me, op1}}
	g = h.playClock(g, 300, 200)
	h.send("GoalScored", map[string]any{"MatchGuid": "R", "Scorer": me.ref()})
	h.send("StatfeedEvent", map[string]any{"MatchGuid": "R", "EventName": "Goal", "MainTarget": me.ref()})
	g.Scores = [2]int{1, 0}
	g.Players[0].Goals = 1
	h.update(g)
	h.tr.Disconnected() // game crashed
	if len(h.saved) != 1 || h.saved[0].Result != "abandoned" {
		t.Fatalf("crash: %+v", h.saved)
	}
	h.advance(2 * time.Minute)
	h.send("MatchInitialized", map[string]any{"MatchGuid": "R"})
	g = h.playClock(g, 150, 0)
	h.send("MatchEnded", map[string]any{"MatchGuid": "R", "WinnerTeamNum": 0})
	if len(h.saved) != 2 {
		t.Fatalf("saved %d", len(h.saved))
	}
	m := h.saved[1]
	if m.GUID != "R" || m.Result != "win" || len(m.Goals) != 1 || !m.Goals[0].MeScored || m.Statfeed["Goal"] != 1 ||
		!m.StartedAt.Equal(start) || m.TeamScore != 1 || m.DurationS != 300 {
		t.Fatalf("resumed %+v", m)
	}
}

// Regression: BallHit events re-emitted during a goal replay were counted.
func TestBallHitDuringReplayIgnored(t *testing.T) {
	h := newHarness(t)
	h.id = Identity{Names: []string{"Me"}}
	g := h.playClock(G{Guid: "BH", Players: []P{me, op1}}, 300, 250)
	hit := map[string]any{"Players": []any{me.ref()}, "Ball": map[string]any{"PostHitSpeed": 2000}}
	h.send("BallHit", hit)
	h.send("GoalScored", map[string]any{"Scorer": me.ref()})
	h.send("GoalReplayStart", map[string]any{})
	h.send("BallHit", hit)
	h.send("BallHit", hit)
	h.send("GoalReplayEnd", map[string]any{})
	h.send("RoundStarted", map[string]any{})
	g.Scores = [2]int{1, 0}
	h.playClock(g, 249, 0)
	h.send("MatchEnded", map[string]any{"WinnerTeamNum": 0})
	if m := h.only(); m.Hits == nil || m.Hits.Count != 1 {
		t.Fatalf("hits %+v", m.Hits)
	}
}

// Regression: goals were attributed to the Scorer's team only; for an own
// goal (Scorer from the conceding team) the team score change is what counts.
func TestOwnGoalAttributedByTeamScore(t *testing.T) {
	for _, updateFirst := range []bool{false, true} {
		h := newHarness(t)
		h.id = Identity{Names: []string{"Me"}}
		g := h.playClock(G{Guid: "OG", Players: []P{me, op1}}, 300, 250)
		goal := map[string]any{"Scorer": me.ref(), "GoalSpeed": 1500}
		if !updateFirst {
			h.send("GoalScored", goal)
		}
		g.Scores = [2]int{0, 1} // orange scored: own goal by Me
		h.update(g)
		if updateFirst {
			h.send("GoalScored", goal)
		}
		h.playClock(g, 249, 0)
		h.send("MatchEnded", map[string]any{"WinnerTeamNum": 1})
		m := h.only()
		if m.TeamScore != 0 || m.OppScore != 1 || m.Goals[0].Team != "them" || m.Goals[0].MeScored || m.Me.Goals != 0 || m.FirstGoal != "them" {
			t.Fatalf("updateFirst=%v: %d-%d goals %+v me %+v", updateFirst, m.TeamScore, m.OppScore, m.Goals, m.Me)
		}
	}
}

// Regression: when tracking starts mid-match (tracker (re)started or join in
// progress), the regulation length was the first clock seen (e.g. 173 s),
// shortening the duration and shifting goal times.
func TestMidMatchJoinIsPartial(t *testing.T) {
	h := newHarness(t)
	h.id = Identity{Names: []string{"Me"}}
	g := h.playClock(G{Guid: "MID", Players: []P{me, op1}, Scores: [2]int{2, 1}}, 173, 100)
	h.send("GoalScored", map[string]any{"Scorer": me.ref()})
	g.Scores = [2]int{3, 1}
	h.playClock(g, 99, 0)
	h.send("MatchEnded", map[string]any{"WinnerTeamNum": 0})
	m := h.only()
	if !m.Partial || m.DurationS != 300 || m.Goals[0].T != 200 || m.TeamScore != 3 || m.OppScore != 1 {
		t.Fatalf("partial=%v dur=%v goals=%+v score %d-%d", m.Partial, m.DurationS, m.Goals, m.TeamScore, m.OppScore)
	}

	// Joined in progress even though MatchCreated was seen (casual).
	h2 := newHarness(t)
	h2.id = h.id
	h2.send("MatchCreated", map[string]any{"MatchGuid": "MID2"})
	h2.playClock(G{Guid: "MID2", Players: []P{me, op1}}, 150, 0)
	h2.send("MatchEnded", map[string]any{"WinnerTeamNum": 0})
	if m := h2.only(); !m.Partial || m.DurationS != 300 {
		t.Fatalf("join in progress: partial=%v dur=%v", m.Partial, m.DurationS)
	}

	// A full match seen from the start is not partial, even a 10-minute one.
	h3 := newHarness(t)
	h3.id = h.id
	h3.send("MatchCreated", map[string]any{"MatchGuid": "FULL"})
	h3.playClock(G{Guid: "FULL", Players: []P{me, op1}}, 600, 0)
	h3.send("MatchEnded", map[string]any{"WinnerTeamNum": 0})
	if m := h3.only(); m.Partial || m.DurationS != 600 {
		t.Fatalf("full: partial=%v dur=%v", m.Partial, m.DurationS)
	}
}

// Regression: a goal whose team is unknown (no scorer, no last touch, no
// score change) was saved as "them".
func TestUnknownTeamGoal(t *testing.T) {
	h := newHarness(t)
	h.id = Identity{Names: []string{"Me"}}
	g := h.playClock(G{Guid: "UK", Players: []P{me, op1}}, 300, 250)
	h.send("GoalScored", map[string]any{"GoalSpeed": 1000})
	h.playClock(g, 249, 0)
	h.send("MatchEnded", map[string]any{"WinnerTeamNum": 0})
	m := h.only()
	if len(m.Goals) != 1 || m.Goals[0].Team != "" || m.OppScore != 0 || m.FirstGoal != "none" {
		t.Fatalf("goals %+v opp %d first %s", m.Goals, m.OppScore, m.FirstGoal)
	}
}

// Regression (real game): GoalScored is re-sent while the goal replay plays,
// which turned a 1-11 loss into 1-23. Goals in replays and duplicates at the
// same game time count once, and UpdateState stays the authoritative score.
func TestGoalReplayDuplicatesIgnored(t *testing.T) {
	h := newHarness(t)
	h.id = Identity{Names: []string{"Me"}}
	g := h.playClock(G{Guid: "DUP", Players: []P{me, op1}}, 300, 280)
	opp := op1
	for i := 0; i < 11; i++ {
		goal := map[string]any{"Scorer": opp.ref(), "GoalSpeed": 90}
		h.send("GoalScored", goal)
		g.Scores[1]++
		opp.Goals++
		g.Players = []P{me, opp}
		h.update(g)
		switch i % 3 {
		case 0: // flagged replay: GoalReplayStart then the re-sent event
			h.send("GoalReplayStart", map[string]any{})
			h.send("GoalScored", goal)
			h.send("GoalReplayEnd", map[string]any{})
		case 1: // replay frames flagged in UpdateState only
			g.Replay = true
			h.update(g)
			h.send("GoalScored", goal)
			g.Replay = false
			h.update(g)
		case 2: // unflagged duplicate at the same (stopped) game time
			h.send("GoalScored", goal)
		}
		h.send("RoundStarted", map[string]any{})
		g = h.playClock(g, 279-i*20, 280-(i+1)*20+1)
	}
	h.send("MatchEnded", map[string]any{"WinnerTeamNum": 1})
	m := h.only()
	if m.TeamScore != 0 || m.OppScore != 11 || len(m.Goals) != 11 {
		t.Fatalf("score %d-%d with %d goals", m.TeamScore, m.OppScore, len(m.Goals))
	}
	if p := m.Players[1]; p.Name != "Opp1" || p.Goals != 11 {
		t.Fatalf("opponent %+v", p)
	}
}

// Even if duplicated GoalScored events slip through, a goal count far above
// the UpdateState score must not replace it.
func TestScoreNotInflatedByGoalCount(t *testing.T) {
	h := newHarness(t)
	h.id = Identity{Names: []string{"Me"}}
	g := h.playClock(G{Guid: "INF", Players: []P{me, op1}}, 300, 290)
	for i := 0; i < 5; i++ { // 5 events at distinct times for 2 real goals
		h.send("GoalScored", map[string]any{"Scorer": op1.ref()})
		g = h.playClock(g, 289-i*10, 280-i*10)
	}
	g.Scores = [2]int{0, 2}
	h.update(g)
	h.playClock(g, 230, 0)
	h.send("MatchEnded", map[string]any{"WinnerTeamNum": 1})
	if m := h.only(); m.TeamScore != 0 || m.OppScore != 2 {
		t.Fatalf("score %d-%d, want 0-2", m.TeamScore, m.OppScore)
	}
}

// Regression (real game): speeds arrive in km/h although documented as uu/s;
// the dashboard showed 2 km/h average speed. Stored speeds are uu/s.
func TestSpeedUnitDetection(t *testing.T) {
	for _, c := range []struct {
		name                    string
		car, hit, goal          float64
		wantCar, wantHit, wantG float64
	}{
		{"kmh", 60, 90, 100, 1666.7, 2500, 2777.8},
		{"uu/s", 1500, 2000, 3000, 1500, 2000, 3000},
	} {
		h := newHarness(t)
		h.id = Identity{Names: []string{"Me"}}
		p := me
		p.Spec = map[string]any{"bHasCar": true, "Speed": c.car, "Boost": 50, "bOnGround": true}
		g := h.playClock(G{Guid: "SPD" + c.name, Players: []P{p, op1}}, 300, 250)
		h.send("BallHit", map[string]any{"Players": []any{me.ref()}, "Ball": map[string]any{"PostHitSpeed": c.hit}})
		h.send("GoalScored", map[string]any{"Scorer": me.ref(), "GoalSpeed": c.goal})
		g.Scores = [2]int{1, 0}
		h.playClock(g, 249, 0)
		h.send("MatchEnded", map[string]any{"WinnerTeamNum": 0})
		m := h.only()
		if m.Movement == nil || m.Hits == nil || len(m.Goals) != 1 {
			t.Fatalf("%s: missing data %+v %+v %+v", c.name, m.Movement, m.Hits, m.Goals)
		}
		if m.Movement.AvgSpeed != c.wantCar || m.Hits.MaxSpeed != c.wantHit || m.Hits.AvgSpeed != c.wantHit || m.Goals[0].Speed != c.wantG {
			t.Fatalf("%s: car %v hit %v/%v goal %v", c.name, m.Movement.AvgSpeed, m.Hits.AvgSpeed, m.Hits.MaxSpeed, m.Goals[0].Speed)
		}
	}
}

// Regression (first real database): the game sends every goal twice at the
// same game time, a full GoalScored and an empty one (no scorer, speed 0),
// sometimes one second apart. The duplicates doubled the scores and shifted
// the team attribution (my goal counted for the opponent).
func TestGoalSentFullAndEmpty(t *testing.T) {
	for _, c := range []struct {
		name       string
		myTeam     int
		emptyFirst bool
	}{{"blue/full-first", 0, false}, {"orange/full-first", 1, false}, {"blue/empty-first", 0, true}} {
		h := newHarness(t)
		h.id = Identity{Names: []string{"Me"}}
		meP, opp := me, op1
		meP.Team, opp.Team = c.myTeam, 1-c.myTeam
		g := h.playClock(G{Guid: "FE" + c.name, Players: []P{meP, opp}}, 300, 290)
		scorers := []P{opp, opp, meP, opp} // real: 1-3
		for i, s := range scorers {
			full := map[string]any{"Scorer": s.ref(), "GoalSpeed": 80}
			empty := map[string]any{"GoalSpeed": 0}
			if c.emptyFirst {
				full, empty = empty, full
			}
			h.send("GoalScored", full)
			if i == 3 { // duplicate on the next whole second
				g.Time--
				h.update(g)
			}
			h.send("GoalScored", empty)
			g.Scores[s.Team]++
			if s.Name == meP.Name {
				meP.Goals++
			} else {
				opp.Goals++
			}
			g.Players = []P{meP, opp}
			h.update(g)
			h.send("RoundStarted", map[string]any{})
			g = h.playClock(g, 289-i*60, 290-(i+1)*60+1)
		}
		h.playClock(g, g.Time, 0)
		h.send("MatchEnded", map[string]any{"WinnerTeamNum": 1 - c.myTeam})
		m := h.only()
		if m.TeamScore != 1 || m.OppScore != 3 || len(m.Goals) != 4 || m.Me.Goals != 1 {
			t.Fatalf("%s: score %d-%d goals %d me %d", c.name, m.TeamScore, m.OppScore, len(m.Goals), m.Me.Goals)
		}
		for i, want := range []string{"them", "them", "us", "them"} {
			if m.Goals[i].Team != want || m.Goals[i].Scorer == "" {
				t.Fatalf("%s: goal %d %+v, want team %s", c.name, i, m.Goals[i], want)
			}
		}
		if !m.Goals[2].MeScored {
			t.Fatalf("%s: my goal not flagged %+v", c.name, m.Goals[2])
		}
	}
}

// Two real goals by the same player a second apart (kickoff goal): the
// kickoff in between proves the second one is new, not a duplicate.
func TestQuickGoalAfterKickoffKept(t *testing.T) {
	h := newHarness(t)
	h.id = Identity{Names: []string{"Me"}}
	g := h.playClock(G{Guid: "QK", Players: []P{me, op1}}, 300, 200)
	goal := map[string]any{"Scorer": me.ref(), "GoalSpeed": 80}
	h.send("GoalScored", goal)
	h.send("GoalScored", map[string]any{"GoalSpeed": 0})
	g.Scores = [2]int{1, 0}
	h.update(g)
	h.send("CountdownBegin", map[string]any{})
	h.send("RoundStarted", map[string]any{})
	g.Time = 199 // one game second later
	h.update(g)
	h.send("GoalScored", goal)
	h.send("GoalScored", map[string]any{"GoalSpeed": 0})
	g.Scores = [2]int{2, 0}
	h.update(g)
	h.playClock(g, 198, 0)
	h.send("MatchEnded", map[string]any{"WinnerTeamNum": 0})
	if m := h.only(); m.TeamScore != 2 || len(m.Goals) != 2 {
		t.Fatalf("score %d-%d goals %d", m.TeamScore, m.OppScore, len(m.Goals))
	}
}
