// Package tracker turns pure game observations into finished matches.
// It is pure logic (no I/O besides the injected callbacks) and uses an
// injected clock so it can be unit tested deterministically.
package tracker

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"rocket-tracker/internal/domain"
)

// Identity is the configured "who am I".
type Identity struct {
	Names []string
	IDs   []string
}

// Options configures a Tracker. Every field is optional.
type Options struct {
	Now            func() time.Time
	Identity       func() Identity
	OnAutoIdentity func(name, primaryID string)
	Save           func(*domain.Match) error
	SaveTraining   func(*domain.Training) error
	DefaultTag     func() string
	Log            *slog.Logger
}

// Rules (exported for tests / docs).
const (
	MinAbandonedGameSeconds = 30.0
	DefaultRegulation       = 300
	// DuelRegulation is the length of an Honor Duel, the only 3-minute match.
	DuelRegulation   = 180
	MaxSampleDT      = 0.5 // seconds
	ClockFrozenAfter = 1500 * time.Millisecond
	// MinTraining: shorter time alone offline (freeplay, packs) is not kept.
	MinTraining = time.Minute
)

// Tracker is safe for concurrent use.
type Tracker struct {
	o  Options
	mu sync.Mutex

	sourceUnit domain.SpeedUnit
	cur        *match
	postEnd    bool   // a match just ended (podium); ignore its trailing updates
	endedGUID  string // raw guid of the last ended match (online: ignored for good)

	// resumable is the last online match finished as abandoned (game crash,
	// disconnect, left to the menu). If the same MatchGuid comes back
	// (reconnect), tracking resumes on it instead of starting from scratch.
	pending   []pendingRecord
	saveErr   error
	resumable *match
}

// New creates a tracker.
func New(o Options) *Tracker {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Identity == nil {
		o.Identity = func() Identity { return Identity{} }
	}
	if o.DefaultTag == nil {
		o.DefaultTag = func() string { return "ranked" }
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &Tracker{o: o}
}

type pstate struct {
	key       string
	name      string
	primaryID string
	shortcut  int
	team      int
	score     int
	goals     int
	shots     int
	assists   int
	saves     int
	touches   int
	demos     int
	targetCnt int
	mv        movementAcc
	firstSeen int
}

type rawGoal struct {
	t         float64
	overtime  bool
	scorer    *domain.PlayerRef
	assister  *domain.PlayerRef
	team      int  // -1 unknown
	confirmed bool // team confirmed by a team score change in UpdateState
	speed     float64
	impact    *domain.Position
}

type rawFeed struct {
	name string
	ref  domain.PlayerRef
	sec  *domain.PlayerRef // the other player (Demolish: the victim)
	t    float64
}

type rawHit struct {
	ref   domain.PlayerRef
	t     float64
	speed float64 // after the touch
	pre   float64
	has   bool
	loc   *domain.Position
}

type match struct {
	speedUnit domain.SpeedUnit
	rawGUID   string
	guid      string
	online    bool
	startedAt time.Time

	players map[string]*pstate
	seq     int

	teamScores map[int]int
	arena      string

	// sawStart: the match start was observed (MatchCreated/Initialized, or a
	// countdown/kickoff before any clock); firstClock: first positive
	// regulation TimeSeconds seen (0 = none yet), -1 if the first clock seen
	// was already overtime.
	sawStart   bool
	firstClock int
	// firstClockGoals: goals already on the scoreboard when firstClock was seen.
	firstClockGoals int

	haveTime       bool
	timeSeconds    int
	overtime       bool
	maxRegTime     int
	otSeconds      int
	lastTimeChange time.Time

	replay    bool
	paused    bool
	roundSeen bool
	inRound   bool

	maxTeamSize int

	updates       int // UpdateState count with a Game object
	replayUpdates int // ... of which bReplay=true
	// Live frames with players on both teams, and those where both teams'
	// cars had their details: a player only gets them for their own team, a
	// spectator for everyone.
	bothTeamsUpdates int
	allCarsUpdates   int
	lastSample       time.Time
	lastUpdate       time.Time

	goals []rawGoal
	// kickoffSinceGoal: a kickoff (countdown or round start) happened after
	// the last goal, so the next GoalScored is a new goal, not a duplicate.
	kickoffSinceGoal bool
	feeds            []rawFeed
	hits             []rawHit

	// Team score increments seen in UpdateState before the matching
	// GoalScored event arrived.
	pendingInc []int
}

// scoreIncrement records that team tn's score went up by one. The scoring
// team is authoritative (own goals: the GoalScored Scorer may belong to the
// conceding team), so it confirms the oldest unconfirmed goal.
func (m *match) scoreIncrement(tn int) {
	for i := range m.goals {
		if !m.goals[i].confirmed {
			m.goals[i].team, m.goals[i].confirmed = tn, true
			return
		}
	}
	if len(m.pendingInc) < 32 {
		m.pendingInc = append(m.pendingInc, tn)
	}
}

func newMatch(rawGUID string, now time.Time) *match {
	m := &match{
		rawGUID:    rawGUID,
		guid:       rawGUID,
		online:     rawGUID != "",
		startedAt:  now,
		players:    map[string]*pstate{},
		teamScores: map[int]int{},
	}
	if m.guid == "" {
		m.guid = "local-" + strconv.FormatInt(now.UnixMilli(), 10)
	}
	return m
}

// training reports whether this is training rather than a match: offline,
// alone on the field (freeplay, training packs, workshop maps).
func (m *match) training() bool { return !m.online && len(m.players) == 1 }

// trained reports whether enough training was seen to be kept: then a match
// starting next is a new one, not this one getting its guid.
func (m *match) trained() bool { return m.training() && m.lastUpdate.Sub(m.startedAt) >= MinTraining }

// progressed reports whether game time has started to elapse.
func (m *match) progressed() bool {
	return m.elapsed() > 0 || len(m.goals) > 0
}

// partial reports whether tracking started mid-match (tracker started or
// restarted during the match, or joined in progress).
func (m *match) partial() bool {
	if !m.sawStart || m.firstClock < 0 {
		return true
	}
	return m.firstClock > 0 && m.firstClock < DefaultRegulation && !m.duelStart()
}

// duelStart reports whether the first clock was the start of an Honor Duel
// (3:00, no goal yet) rather than a 5-minute match joined at 3:00 left, which
// can only be seen at 0-0 by joining during that very second.
func (m *match) duelStart() bool {
	return m.firstClock == DuelRegulation && m.firstClockGoals == 0
}

func (m *match) regulation() int {
	if m.partial() {
		// Joined mid-way: the max clock seen is not the match length. No
		// Rocket League match is shorter than 5 minutes.
		return max(m.maxRegTime, DefaultRegulation)
	}
	if m.maxRegTime > 0 {
		return m.maxRegTime
	}
	return DefaultRegulation
}

func (m *match) elapsed() float64 {
	if !m.haveTime {
		return 0
	}
	if m.overtime {
		return float64(m.regulation() + m.timeSeconds)
	}
	e := m.regulation() - m.timeSeconds
	if e < 0 {
		e = 0
	}
	return float64(e)
}

func (m *match) setClock(ts int, ot bool, now time.Time) {
	if ts < 0 {
		ts = 0
	}
	if m.firstClock == 0 {
		switch {
		case ot:
			m.firstClock = -1
		case ts > 0:
			m.firstClock = ts
			for _, s := range m.teamScores {
				m.firstClockGoals += s
			}
		}
	}
	if ot {
		if !m.overtime {
			m.overtime = true
		}
		if ts > m.otSeconds {
			m.otSeconds = ts
		}
	} else if !m.overtime && ts > m.maxRegTime {
		m.maxRegTime = ts
	}
	if !m.haveTime || ts != m.timeSeconds {
		m.lastTimeChange = now
	}
	if m.overtime && !ot {
		// Never go back from overtime within a match; ignore stale samples.
		return
	}
	m.haveTime = true
	m.timeSeconds = ts
}

func (m *match) clockRunning(now time.Time) bool {
	if m.replay || m.paused || !m.haveTime {
		return false
	}
	if m.roundSeen && !m.inRound {
		return false
	}
	return now.Sub(m.lastTimeChange) <= ClockFrozenAfter
}

func playerKey(p *domain.ObservedPlayer) string {
	if p.PrimaryID != "" {
		return string(p.PrimaryID) + "/" + string(p.Name)
	}
	return string(p.Name) + "#" + strconv.Itoa(int(p.TeamNum)) + "#" + strconv.Itoa(int(p.Shortcut))
}

// resolve finds the player referenced by Name+Shortcut+TeamNum.
func (m *match) resolve(ref *domain.PlayerRef) *pstate {
	if ref == nil || ref.Name == "" {
		return nil
	}
	name := string(ref.Name)
	var byNameTeam, byName *pstate
	for _, p := range m.sortedPlayers() {
		if p.name != name {
			continue
		}
		if p.team == int(ref.TeamNum) {
			if p.shortcut == int(ref.Shortcut) {
				return p
			}
			if byNameTeam == nil {
				byNameTeam = p
			}
		}
		if byName == nil {
			byName = p
		}
	}
	if byNameTeam != nil {
		return byNameTeam
	}
	return byName
}

func (m *match) sortedPlayers() []*pstate {
	out := make([]*pstate, 0, len(m.players))
	for _, p := range m.players {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].firstSeen < out[j].firstSeen })
	return out
}

// spectating reports whether the match is watched rather than played: most
// live frames gave the details of every car, both teams included.
func (m *match) spectating() bool {
	return m.allCarsUpdates >= 10 && 2*m.allCarsUpdates > m.bothTeamsUpdates
}

// resolveMe applies the identity precedence. source is "id", "name" or "auto".
// When spectating, no camera target is "me": the watched player is not.
func (m *match) resolveMe(id Identity) (p *pstate, source string) {
	players := m.sortedPlayers()
	for _, want := range id.IDs {
		for _, pl := range players {
			if pl.primaryID != "" && strings.EqualFold(pl.primaryID, strings.TrimSpace(want)) {
				return pl, "id"
			}
		}
	}
	for _, want := range id.Names {
		for _, pl := range players {
			if strings.EqualFold(pl.name, strings.TrimSpace(want)) {
				return pl, "name"
			}
		}
	}
	if m.spectating() {
		return nil, ""
	}
	var best *pstate
	for _, pl := range players {
		if pl.targetCnt > 0 && (best == nil || pl.targetCnt > best.targetCnt) {
			best = pl
		}
	}
	if best != nil {
		return best, "auto"
	}
	return nil, ""
}

// HandleEvent processes one event. It never panics.
func (t *Tracker) HandleEvent(ev domain.Observation) {
	t.mu.Lock()
	defer t.mu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			t.o.Log.Error("tracker: recovered panic", "event", ev.Name, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
		}
	}()
	unit := ev.SpeedUnit
	if !unit.Valid() {
		t.o.Log.Warn("tracker: unknown source speed unit", "unit", unit)
		return
	}
	if unit == "" {
		unit = domain.SpeedUnknown
	}
	if t.cur != nil && t.cur.speedUnit != unit {
		t.o.Log.Warn("tracker: source speed unit changed during match", "current", t.cur.speedUnit, "incoming", unit)
		return
	}
	t.retryPending()
	t.sourceUnit = unit
	now := t.o.Now()
	switch ev.Name {
	case domain.EvMatchCreated, domain.EvMatchInitialized:
		t.onMatchStart(ev.MatchGUID, now)
	case domain.EvUpdateState:
		if ev.State != nil {
			t.onUpdate(ev.State, now)
		}
	case domain.EvClockUpdatedSeconds:
		d := ev.Clock
		if d == nil {
			return
		}
		if t.cur != nil && d.TimeSeconds != nil && t.sameMatch(string(d.MatchGUID)) {
			t.cur.setClock(int(*d.TimeSeconds), bool(d.Overtime), now)
		}
	case domain.EvCountdownBegin:
		if t.cur != nil {
			if !t.cur.haveTime {
				t.cur.sawStart = true
			}
			t.cur.roundSeen = true
			t.cur.inRound = false
			t.cur.kickoffSinceGoal = true
		}
	case domain.EvRoundStarted:
		if t.cur != nil {
			if !t.cur.haveTime {
				t.cur.sawStart = true
			}
			t.cur.roundSeen = true
			t.cur.inRound = true
			t.cur.replay = false
			t.cur.kickoffSinceGoal = true
		}
	case domain.EvGoalReplayStart:
		if t.cur != nil {
			t.cur.replay = true
		}
	case domain.EvGoalReplayEnd:
		if t.cur != nil {
			t.cur.replay = false
		}
	case domain.EvMatchPaused:
		if t.cur != nil {
			t.cur.paused = true
		}
	case domain.EvMatchUnpaused:
		if t.cur != nil {
			t.cur.paused = false
		}
	case domain.EvGoalScored:
		if ev.Goal != nil {
			t.onGoal(ev.Goal)
		}
	case domain.EvStatfeedEvent:
		d := ev.Feed
		if d == nil {
			return
		}
		if t.cur != nil && d.EventName != "" && d.MainTarget.Valid() && t.sameMatch(string(d.MatchGUID)) {
			f := rawFeed{name: string(d.EventName), ref: *d.MainTarget, t: t.cur.elapsed()}
			if d.SecondaryTarget.Valid() {
				sec := *d.SecondaryTarget
				f.sec = &sec
			}
			t.cur.feeds = append(t.cur.feeds, f)
		}
	case domain.EvBallHit:
		if ev.Hit != nil {
			t.onBallHit(ev.Hit)
		}
	case domain.EvMatchEnded:
		if ev.End != nil {
			t.onMatchEnded(ev.End, now)
		}
	case domain.EvMatchDestroyed:
		guid := ev.MatchGUID
		// A late MatchDestroyed for a previous match (e.g. "play again":
		// the next match was created first) must not kill the current one.
		stale := guid != "" && t.cur != nil && guid != t.cur.rawGUID && (t.cur.rawGUID != "" || guid == t.endedGUID)
		if t.cur != nil && !stale {
			t.finish("abandoned", nil, now, "match destroyed before MatchEnded")
		}
		t.postEnd = false
	default:
		// PodiumStart, ReplayCreated, CrossbarHit, GoalReplayWillEnd, unknown: nothing.
	}
}

// sameMatch: events carrying a guid that differs from the current match are
// ignored (empty guid always matches).
func (t *Tracker) sameMatch(guid string) bool {
	return guid == "" || t.cur == nil || t.cur.rawGUID == "" || guid == t.cur.rawGUID
}

func (t *Tracker) onMatchStart(guid string, now time.Time) {
	if guid != "" && guid == t.endedGUID {
		return // late start event of an already ended match
	}
	t.postEnd = false
	if t.cur != nil {
		switch {
		case guid != "" && guid == t.cur.rawGUID:
			return // Initialized after Created
		case guid == "" && t.cur.rawGUID == "" && !t.cur.progressed():
			return
		case guid != "" && t.cur.rawGUID == "" && !t.cur.progressed() && !t.cur.trained():
			t.cur.adoptGUID(guid)
			return
		}
		t.finish("abandoned", nil, now, "new match started")
	}
	t.start(guid, now)
	if !t.cur.haveTime {
		t.cur.sawStart = true
	}
}

func (t *Tracker) start(guid string, now time.Time) {
	if r := t.resumable; r != nil && guid != "" && guid == r.rawGUID && r.speedUnit == t.sourceUnit {
		t.resumable = nil
		r.resume()
		t.cur = r
		t.o.Log.Info("match resumed (reconnected)", "guid", r.guid)
		return
	}
	t.resumable = nil
	t.cur = newMatch(guid, now)
	t.cur.speedUnit = t.sourceUnit
	t.o.Log.Info("match started", "guid", t.cur.guid, "online", t.cur.online)
}

// resume resets the transient state of a match picked up again after a
// reconnect (no RoundStarted will necessarily be re-sent mid-play).
func (m *match) resume() {
	m.replay, m.paused = false, false
	m.roundSeen, m.inRound = false, false
	m.lastSample = time.Time{}
}

func (m *match) adoptGUID(guid string) {
	m.rawGUID = guid
	m.guid = guid
	m.online = true
}

func (t *Tracker) onUpdate(d *domain.StateObservation, now time.Time) {
	guid := string(d.MatchGUID)
	if guid != "" && guid == t.endedGUID {
		// Podium / scoreboard of the ended online match, possibly even after
		// MatchDestroyed: never restart it (it would later be saved again
		// as "abandoned" over the real result).
		return
	}
	if t.postEnd {
		if guid == "" {
			return // podium / scoreboard after an offline MatchEnded
		}
		t.postEnd = false
	}
	if t.cur == nil {
		if len(d.Players) == 0 && d.Game == nil {
			return
		}
		t.start(guid, now)
	} else if guid != t.cur.rawGUID {
		switch {
		case t.cur.rawGUID == "" && guid != "" && !t.cur.trained():
			t.cur.adoptGUID(guid)
		case t.cur.rawGUID != "" && guid == "":
			// Transient empty guid during an online match: keep it.
		default:
			t.finish("abandoned", nil, now, "guid changed")
			t.start(guid, now)
		}
	}
	m := t.cur
	m.lastUpdate = now

	if g := d.Game; g != nil {
		if g.Arena != "" {
			m.arena = string(g.Arena)
		}
		for _, tm := range g.Teams {
			tn, s := int(tm.TeamNum), int(tm.Score)
			if old, ok := m.teamScores[tn]; ok && s > old {
				for k := 0; k < s-old && k < 16; k++ {
					m.scoreIncrement(tn)
				}
			}
			m.teamScores[tn] = s
		}
		m.replay = bool(g.Replay)
		m.updates++
		if m.replay {
			m.replayUpdates++
		}
		if g.TimeSeconds != nil {
			m.setClock(int(*g.TimeSeconds), bool(g.Overtime), now)
		} else if g.Overtime {
			m.overtime = true
		}
	}

	perTeam := map[int]int{}
	for i := range d.Players {
		sp := &d.Players[i]
		if sp.Name == "" && sp.PrimaryID == "" {
			continue
		}
		k := playerKey(sp)
		p := m.players[k]
		if p == nil {
			m.seq++
			p = &pstate{key: k, firstSeen: m.seq}
			m.players[k] = p
		}
		p.name = string(sp.Name)
		p.primaryID = string(sp.PrimaryID)
		p.shortcut = int(sp.Shortcut)
		p.team = int(sp.TeamNum)
		p.score = int(sp.Score)
		p.goals = int(sp.Goals)
		p.shots = int(sp.Shots)
		p.assists = int(sp.Assists)
		p.saves = int(sp.Saves)
		p.touches = int(sp.Touches)
		p.demos = int(sp.Demos)
		if p.team == 0 || p.team == 1 {
			perTeam[p.team]++
		}
	}
	for _, n := range perTeam {
		if n > m.maxTeamSize {
			m.maxTeamSize = n
		}
	}

	if g := d.Game; g != nil && bool(g.HasTarget) && !bool(g.Replay) && g.Target.Valid() {
		if p := m.resolve(g.Target); p != nil {
			p.targetCnt++
		}
	}

	if !m.replay && m.clockRunning(now) {
		var seen, detailed [2]bool
		for i := range d.Players {
			sp := &d.Players[i]
			if tn := int(sp.TeamNum); tn == 0 || tn == 1 {
				seen[tn] = true
				detailed[tn] = detailed[tn] || sp.HasSpectatorFields()
			}
		}
		if seen[0] && seen[1] {
			m.bothTeamsUpdates++
			if detailed[0] && detailed[1] {
				m.allCarsUpdates++
			}
		}
	}

	// Movement sampling requires car details or an observed demolition.
	dt := 0.0
	if !m.lastSample.IsZero() {
		dt = now.Sub(m.lastSample).Seconds()
	}
	m.lastSample = now
	if dt > MaxSampleDT {
		dt = MaxSampleDT
	}
	if dt > 0 && m.clockRunning(now) {
		for i := range d.Players {
			sp := &d.Players[i]
			if !sp.HasSpectatorFields() && sp.Demolished == nil {
				continue
			}
			if p := m.players[playerKey(sp)]; p != nil {
				p.mv.add(sp, dt)
			}
		}
	}
}

func (t *Tracker) onGoal(d *domain.GoalObservation) {
	m := t.cur
	if m == nil || !t.sameMatch(string(d.MatchGUID)) {
		return
	}
	if m.replay {
		return // the game re-sends GoalScored while replaying a goal
	}
	g := rawGoal{t: m.elapsed(), overtime: m.overtime, team: -1, speed: float64(d.GoalSpeed), impact: d.ImpactLocation}
	if d.Scorer.Valid() {
		s := *d.Scorer
		g.scorer = &s
		g.team = int(s.TeamNum)
	} else if d.LastTouch.Valid() {
		g.team = int(d.LastTouch.TeamNum)
	}
	// The game sends each goal twice: a full GoalScored and an empty one (no
	// scorer, speed 0), at the same game time. The clock is stopped from a
	// goal until the next kickoff, so goals that close are the same goal: keep
	// one, completed with whatever the other one carries.
	if n := len(m.goals); n > 0 && m.haveTime && !m.kickoffSinceGoal {
		if last := &m.goals[n-1]; math.Abs(last.t-g.t) < dupGoalWindow && (last.scorer == nil || g.scorer == nil || sameRef(last.scorer, g.scorer)) {
			if last.scorer == nil && g.scorer != nil {
				last.scorer = g.scorer
				if !last.confirmed {
					last.team = g.team
				}
			}
			if last.assister == nil {
				last.assister = g.assister
			}
			last.speed = max(last.speed, g.speed)
			if last.impact == nil {
				last.impact = g.impact
			}
			return
		}
	}
	if d.Assister.Valid() {
		a := *d.Assister
		g.assister = &a
	}
	if len(m.pendingInc) > 0 { // the score update came first
		g.team, g.confirmed = m.pendingInc[0], true
		m.pendingInc = m.pendingInc[1:]
	}
	m.goals = append(m.goals, g)
	m.kickoffSinceGoal = false
	if m.roundSeen {
		m.inRound = false // clock stops until next kickoff
	}
}

func (t *Tracker) onBallHit(d *domain.HitObservation) {
	m := t.cur
	if m == nil || !t.sameMatch(string(d.MatchGUID)) {
		return
	}
	if m.replay {
		return // goal replays replay the touches: do not count them twice
	}
	refs := []domain.PlayerRef(d.Players)
	if len(refs) == 0 && d.Player.Valid() {
		refs = append(refs, *d.Player)
	}
	for _, r := range refs {
		if r.Name == "" {
			continue
		}
		h := rawHit{ref: r, t: m.elapsed()}
		if d.Ball != nil {
			h.speed, h.pre, h.has = float64(d.Ball.PostHitSpeed), float64(d.Ball.PreHitSpeed), true
			h.loc = d.Ball.Location
		}
		m.hits = append(m.hits, h)
	}
	if len(m.hits) > 100000 { // absurd stream: keep memory bounded
		m.hits = m.hits[len(m.hits)-50000:]
	}
}

func (t *Tracker) onMatchEnded(d *domain.EndObservation, now time.Time) {
	if t.cur == nil {
		return
	}
	if !t.sameMatch(string(d.MatchGUID)) {
		return
	}
	var winner *int
	if d.WinnerTeamNum != nil {
		w := int(*d.WinnerTeamNum)
		winner = &w
	}
	raw := t.cur.rawGUID
	t.finish("ended", winner, now, "match ended")
	t.postEnd = true
	t.endedGUID = raw
}

// Disconnected must be called when the connection to the game drops.
func (t *Tracker) Disconnected() {
	t.mu.Lock()
	defer t.mu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			t.o.Log.Error("tracker: recovered panic on disconnect", "panic", fmt.Sprint(r))
		}
	}()
	if t.cur != nil {
		t.finish("abandoned", nil, t.o.Now(), "disconnected from game")
	}
	t.postEnd = false
}

// Flush abandons the current match (used at shutdown).
func (t *Tracker) Flush() error {
	t.Disconnected()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.retryPending()
	return t.saveErr
}

// PendingRecords and SaveError expose recording failures independently of game
// state. Pending snapshots live until commit; Flush retries them at shutdown.
func (t *Tracker) PendingRecords() int { t.mu.Lock(); defer t.mu.Unlock(); return len(t.pending) }
func (t *Tracker) SaveError() error    { t.mu.Lock(); defer t.mu.Unlock(); return t.saveErr }

type pendingRecord struct {
	result   *domain.Match
	training *domain.Training
}

func (t *Tracker) retryPending() {
	pending := t.pending
	t.pending = nil
	var failures []error
	for _, p := range pending {
		if err := t.savePending(p); err != nil {
			t.pending = append(t.pending, p)
			failures = append(failures, err)
		}
	}
	t.saveErr = errors.Join(failures...)
	if t.saveErr != nil {
		t.o.Log.Error("recording failed; snapshots retained for retry", "pending", len(t.pending), "err", t.saveErr)
	}
}

func (t *Tracker) savePending(p pendingRecord) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("tracker: recording panicked; snapshot retained: %v", v)
		}
	}()
	// Each attempt owns a deep copy. A failed record cannot block another
	// independent result, including a permanently invalid training snapshot.
	if p.training != nil {
		if t.o.SaveTraining != nil {
			return t.o.SaveTraining(cloneTraining(p.training))
		}
	} else if t.o.Save != nil {
		return t.o.Save(cloneResult(p.result))
	}
	return nil
}

// finish builds and saves the current match. kind is "ended" or "abandoned".
func (t *Tracker) finish(kind string, winner *int, now time.Time, why string) {
	m := t.cur
	t.cur = nil
	if m == nil {
		return
	}
	if kind == "abandoned" && m.online {
		t.resumable = m
	}
	if m.training() {
		t.saveTraining(m)
		return
	}
	res, reason := t.build(m, kind, winner, now)
	if res == nil {
		t.o.Log.Info("match not saved", "guid", m.guid, "why", why, "reason", reason)
		return
	}
	t.pending = append(t.pending, pendingRecord{result: cloneResult(res)})
	t.retryPending()

}

// saveTraining keeps a training (time alone offline) long enough to count.
func (t *Tracker) saveTraining(m *match) {
	tr := t.buildTraining(m)
	if tr == nil {
		t.o.Log.Info("training not saved", "seconds", m.lastUpdate.Sub(m.startedAt).Seconds())
		return
	}
	t.pending = append(t.pending, pendingRecord{training: cloneTraining(tr)})
	t.retryPending()

}

func (t *Tracker) buildTraining(m *match) *domain.Training {
	if !m.trained() {
		return nil
	}
	var p *pstate
	for _, x := range m.players {
		p = x
	}
	tr := &domain.Training{SpeedUnit: m.speedUnit, StartedAt: m.startedAt, EndedAt: m.lastUpdate, Source: "game", Arena: m.arena}
	if id := strings.ToLower(strings.TrimSpace(p.primaryID)); id != "" {
		tr.Profile = id
	} else if n := strings.ToLower(strings.TrimSpace(p.name)); n != "" {
		tr.Profile = "name:" + n
	}
	var sum, top float64
	n := 0
	for _, h := range m.hits {
		tr.Touches++
		if h.has {
			n++
			sum += h.speed
			top = max(top, h.speed)
		}
	}
	if n > 0 {
		tr.AvgHitSpeed, tr.MaxHitSpeed = round1(sum/float64(n)), round1(top)
	}
	return tr
}

// Build is exposed for tests: it computes the match record without saving.
func (t *Tracker) build(m *match, kind string, winner *int, now time.Time) (*domain.Match, string) {
	if len(m.players) == 0 {
		return nil, "no players"
	}
	me, src := m.resolveMe(t.o.Identity())
	if me == nil {
		return nil, "me not found (spectating?)"
	}
	if !m.online && len(m.players) <= 1 {
		return nil, "offline with a single player (freeplay/training)"
	}
	if m.updates >= 20 && float64(m.replayUpdates) > 0.9*float64(m.updates) {
		return nil, "almost only replay frames (watching a replay file?)"
	}
	elapsed := m.elapsed()
	if kind == "abandoned" && elapsed < MinAbandonedGameSeconds {
		return nil, fmt.Sprintf("abandoned after only %.0fs of game time", elapsed)
	}

	myTeam := me.team
	out := &domain.Match{
		SpeedUnit: m.speedUnit,
		GUID:      m.guid,
		Online:    m.online,
		StartedAt: m.startedAt,
		EndedAt:   now,
		DurationS: elapsed,
		Overtime:  m.overtime,
		Arena:     m.arena,
		MyTeam:    myTeam,
		Partial:   m.partial(),
		Tag:       t.o.DefaultTag(),
	}
	if !out.Partial {
		out.RegulationS = m.regulation()
	}
	if m.overtime {
		out.OvertimeS = float64(m.otSeconds)
	}

	// Scores.
	if len(m.teamScores) > 0 {
		out.TeamScore = m.teamScores[myTeam]
		for tn, s := range m.teamScores {
			if tn != myTeam {
				out.OppScore += s
			}
		}
	} else {
		for _, p := range m.players {
			if p.team == myTeam {
				out.TeamScore += p.goals
			} else {
				out.OppScore += p.goals
			}
		}
	}
	// The last UpdateState may predate the final goal (e.g. an overtime
	// winner immediately followed by MatchEnded), so GoalScored events can add
	// that one goal. UpdateState stays authoritative otherwise: any larger gap
	// means duplicated GoalScored events, not missed score updates.
	goalsUs, goalsThem := 0, 0
	scored := map[*pstate]int{}
	for _, g := range m.goals {
		switch {
		case g.team == myTeam:
			goalsUs++
		case g.team >= 0:
			goalsThem++
		}
		if p := m.resolve(g.scorer); p != nil && p.team == g.team {
			scored[p]++
		}
	}
	out.TeamScore = addFinalGoal(out.TeamScore, goalsUs)
	out.OppScore = addFinalGoal(out.OppScore, goalsThem)
	for p, n := range scored {
		if g := addFinalGoal(p.goals, n); g > p.goals {
			p.score += 100 * (g - p.goals)
			p.goals = g
		}
	}

	// Result.
	switch {
	case kind == "abandoned":
		out.Result = "abandoned"
	case winner != nil && (*winner == 0 || *winner == 1):
		if *winner == myTeam {
			out.Result = "win"
		} else {
			out.Result = "loss"
		}
	case out.TeamScore > out.OppScore:
		out.Result = "win"
	default:
		out.Result = "loss"
	}
	out.Forfeit = kind == "ended" && m.haveTime && !m.overtime && m.timeSeconds > 0

	// Mode / variant.
	ts := m.maxTeamSize
	if ts <= 0 {
		ts = 1
	}
	out.TeamSize = ts
	out.Mode = fmt.Sprintf("%dv%d", ts, ts)
	la := strings.ToLower(m.arena)
	switch {
	case strings.Contains(la, "hoops"):
		out.Variant = "Hoops"
	case strings.Contains(la, "shattershot"):
		out.Variant = "Dropshot"
	default:
		out.Variant = "Soccar"
	}

	// Players.
	maxMyTeamScore := math.MinInt
	for _, p := range m.sortedPlayers() {
		isMe := p == me
		out.Players = append(out.Players, domain.Player{
			Name: p.name, PrimaryID: p.primaryID, Team: p.team, Score: p.score, Goals: p.goals,
			Shots: p.shots, Assists: p.assists, Saves: p.saves, Touches: p.touches, Demos: p.demos, IsMe: isMe,
		})
		if p.team == myTeam && p.score > maxMyTeamScore {
			maxMyTeamScore = p.score
		}
	}
	out.Me = domain.MeStats{Name: me.name, PrimaryID: me.primaryID, Score: me.score, Goals: me.goals, Shots: me.shots,
		Assists: me.assists, Saves: me.saves, Touches: me.touches, Demos: me.demos}

	out.Movement = me.mv.result()

	// Hits.
	var hc, hsc int
	var hsum, hmax float64
	var mine []rawHit
	for _, h := range m.hits {
		if m.resolve(&h.ref) != me {
			continue
		}
		hc++
		mine = append(mine, h)
		if h.has {
			hsc++
			hsum += h.speed
			if h.speed > hmax {
				hmax = h.speed
			}
		}
	}
	if hc > 0 {
		out.Hits = &domain.Hits{Count: hc, MaxSpeed: round1(hmax)}
		if hsc > 0 {
			out.Hits.AvgSpeed = round1(hsum / float64(hsc))
		}
		for _, h := range mine {
			if h.loc == nil || !h.has {
				continue
			}
			v := fromSide(h.loc, myTeam)
			out.Hits.Points = append(out.Hits.Points, domain.HitPoint{T: round1(h.t), X: v.X, Y: v.Y, Z: v.Z,
				Pre: whole(h.pre), Post: whole(h.speed)})
		}
	}

	// Goals.
	for _, g := range m.goals {
		sg := domain.Goal{T: g.t, Speed: round1(g.speed), Overtime: g.overtime}
		switch {
		case g.team == myTeam:
			sg.Team = "us"
		case g.team >= 0:
			sg.Team = "them"
		}
		if g.scorer != nil {
			sg.Scorer = string(g.scorer.Name)
			sg.MeScored = m.resolve(g.scorer) == me && g.team == myTeam // not an own goal
		}
		if g.assister != nil {
			sg.Assister = string(g.assister.Name)
			sg.MeAssist = m.resolve(g.assister) == me
		}
		if g.impact != nil {
			v := fromSide(g.impact, myTeam)
			sg.Impact = &v
		}
		out.Goals = append(out.Goals, sg)
	}

	// Statfeed.
	out.Statfeed = map[string]int{}
	for _, f := range m.feeds {
		by := m.resolve(&f.ref)
		if by == me {
			out.Statfeed[f.name]++
		}
		if k := domain.EventKind(f.name); k != "" {
			team := "them"
			if feedTeam(by, &f.ref) == myTeam {
				team = "us"
			}
			out.Events = append(out.Events, domain.Event{T: round1(f.t), Kind: k, Team: team, Me: by == me})
		}
		if f.name != "Demolish" || f.sec == nil {
			continue
		}
		switch victim := m.resolve(f.sec); {
		case by == me && victim != nil && victim != me:
			out.Demolitions = append(out.Demolitions, domain.Demolition{T: round1(f.t), Player: victim.name, Mine: true})
		case victim == me && by != nil && by != me:
			out.Demolitions = append(out.Demolitions, domain.Demolition{T: round1(f.t), Player: by.name})
		}
	}
	out.MVP = out.Statfeed["MVP"] > 0 || (out.Result == "win" && me.score >= maxMyTeamScore)

	out.DurationS = round1(out.DurationS)
	out.Normalize()

	if src == "auto" && m.online && t.o.OnAutoIdentity != nil {
		id := t.o.Identity()
		if len(id.IDs) == 0 && len(id.Names) == 0 {
			t.o.OnAutoIdentity(me.name, me.primaryID)
		}
	}
	return out, ""
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }

// fromSide returns a field position seen from team's side: the blue team
// (0) defends negative Y, so the orange team's positions are turned around.
func fromSide(p *domain.Position, team int) domain.Vec {
	v := domain.Vec{X: whole(float64(p.X)), Y: whole(float64(p.Y)), Z: whole(float64(p.Z))}
	if team == 1 {
		v.X, v.Y = 0-v.X, 0-v.Y
	}
	return v
}

// whole rounds to the unit, never to -0 (which would be stored as "-0").
func whole(f float64) float64 { return math.Round(f) + 0 }

// addFinalGoal returns the UpdateState score, plus one when the GoalScored
// count shows exactly one more goal (the final goal, scored after the last
// UpdateState).
func addFinalGoal(score, counted int) int {
	if counted == score+1 {
		return counted
	}
	return score
}

// dupGoalWindow is the game-time window (s) within which two GoalScored
// events are the same goal: the clock is stopped until the next kickoff, but
// the duplicate may land on the next whole second.
const dupGoalWindow = 2

// feedTeam is the team of a statfeed event's player: the resolved player's,
// else the team number the event carries.
func feedTeam(p *pstate, ref *domain.PlayerRef) int {
	if p != nil {
		return p.team
	}
	return ref.TeamNum
}

// sameRef reports whether two optional player references are the same player.
func sameRef(a, b *domain.PlayerRef) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Name == b.Name && a.TeamNum == b.TeamNum
}

// Live is the in-match snapshot exposed in /api/status.
type Live struct {
	SpeedUnit   domain.SpeedUnit `json:"speed_unit"`
	GUID        string           `json:"guid"`
	Mode        string           `json:"mode"`
	Arena       string           `json:"arena"`
	TimeSeconds int              `json:"time_seconds"`
	Overtime    bool             `json:"overtime"`
	TeamScore   int              `json:"team_score"`
	OppScore    int              `json:"opp_score"`
	MyTeam      int              `json:"my_team"`
	Me          *LiveMe          `json:"me"`
	// ElapsedS is the game time played (s), overtime included; Partial: the
	// tracking started mid-match (goals before it are missing).
	ElapsedS float64 `json:"elapsed_s"`
	Partial  bool    `json:"partial,omitempty"`
	// RegulationS is the regulation length (180 s for an Honor Duel), 0 when
	// partial or before the clock.
	RegulationS int `json:"regulation_s,omitempty"`
	// Training: alone offline (freeplay, packs) since StartedAt.
	Training  bool      `json:"training,omitempty"`
	StartedAt time.Time `json:"started_at"`
	// Players are the others in the match (the dashboard looks them up in
	// the history: opponents already met, usual teammates).
	Players []LivePlayer `json:"players"`
	Goals   []LiveGoal   `json:"goals"`
	// Events are the timed shots and saves so far, by either team.
	Events []LiveEvent `json:"events"`
}

// LiveEvent is a shot or a save of the live match. Team is the raw team
// number; Me: made by me.
type LiveEvent struct {
	T    float64 `json:"t"`
	Kind string  `json:"kind"` // shot | save
	Team int     `json:"team"`
	Me   bool    `json:"me,omitempty"`
}

type LivePlayer struct {
	Name      string `json:"name"`
	PrimaryID string `json:"primary_id"`
	Team      int    `json:"team"`
	LiveStats
}

// LiveStats are a player's scoreboard numbers.
type LiveStats struct {
	Score   int `json:"score"`
	Goals   int `json:"goals"`
	Shots   int `json:"shots"`
	Assists int `json:"assists"`
	Saves   int `json:"saves"`
	Touches int `json:"touches"`
	Demos   int `json:"demos"`
}

type LiveMe struct {
	Name string `json:"name"`
	LiveStats
	// Movement so far (nil without accepted car or demolition observations).
	Movement *domain.Movement `json:"movement,omitempty"`
}

// LiveGoal is a goal of the live match. Team is the raw team number (-1
// unknown); Speed is as sent by the game (Live.SpeedUnit).
type LiveGoal struct {
	T        float64 `json:"t"`
	Team     int     `json:"team"`
	Scorer   string  `json:"scorer"`
	Assister string  `json:"assister"`
	Speed    float64 `json:"speed"`
	Overtime bool    `json:"overtime"`
}

func (p *pstate) liveStats() LiveStats {
	return LiveStats{Score: p.score, Goals: p.goals, Shots: p.shots, Assists: p.assists, Saves: p.saves, Touches: p.touches, Demos: p.demos}
}

// Live returns the current match snapshot, or nil.
func (t *Tracker) Live() *Live {
	t.mu.Lock()
	defer t.mu.Unlock()
	m := t.cur
	if m == nil {
		return nil
	}
	ts := m.maxTeamSize
	if ts <= 0 {
		ts = 1
	}
	l := &Live{SpeedUnit: m.speedUnit, GUID: m.guid, Mode: fmt.Sprintf("%dv%d", ts, ts), Arena: m.arena, TimeSeconds: m.timeSeconds, Overtime: m.overtime,
		ElapsedS: m.elapsed(), Partial: m.partial(), Training: m.training(), StartedAt: m.startedAt.UTC()}
	if !l.Partial && m.haveTime {
		l.RegulationS = m.regulation()
	}
	me, _ := m.resolveMe(t.o.Identity())
	if me != nil {
		l.MyTeam = me.team
		l.Me = &LiveMe{Name: me.name, LiveStats: me.liveStats(), Movement: me.mv.result()}
	}
	for tn, s := range m.teamScores {
		if tn == l.MyTeam {
			l.TeamScore = s
		} else {
			l.OppScore += s
		}
	}
	l.Players = []LivePlayer{}
	for _, p := range m.sortedPlayers() {
		if p != me {
			l.Players = append(l.Players, LivePlayer{Name: p.name, PrimaryID: p.primaryID, Team: p.team, LiveStats: p.liveStats()})
		}
	}
	l.Goals = []LiveGoal{}
	for _, g := range m.goals {
		lg := LiveGoal{T: g.t, Team: g.team, Speed: g.speed, Overtime: g.overtime}
		if g.scorer != nil {
			lg.Scorer = string(g.scorer.Name)
		}
		if g.assister != nil {
			lg.Assister = string(g.assister.Name)
		}
		l.Goals = append(l.Goals, lg)
	}
	l.Events = []LiveEvent{}
	for _, f := range m.feeds {
		if k := domain.EventKind(f.name); k != "" {
			by := m.resolve(&f.ref)
			l.Events = append(l.Events, LiveEvent{T: round1(f.t), Kind: k, Team: feedTeam(by, &f.ref), Me: me != nil && by == me})
		}
	}
	return l
}

// InMatch reports whether a match is being tracked.
func (t *Tracker) InMatch() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cur != nil
}
