// Package tracker turns the Stats API event stream into finished matches.
// It is pure logic (no I/O besides the injected callbacks) and uses an
// injected clock so it can be unit tested deterministically.
package tracker

import (
	"fmt"
	"log/slog"
	"math"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"rocket-tracker/internal/statsapi"
	"rocket-tracker/internal/store"
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
	Save           func(*store.Match) error
	DefaultTag     func() string
	Log            *slog.Logger
}

// Rules (exported for tests / docs).
const (
	MinAbandonedGameSeconds = 30.0
	DefaultRegulation       = 300
	MaxSampleDT             = 0.5 // seconds
	ClockFrozenAfter        = 1500 * time.Millisecond
)

// Tracker is safe for concurrent use.
type Tracker struct {
	o  Options
	mu sync.Mutex

	cur       *match
	postEnd   bool   // a match just ended (podium); ignore its trailing updates
	endedGUID string // raw guid of the last ended match (online: ignored for good)

	// resumable is the last online match finished as abandoned (game crash,
	// disconnect, left to the menu). If the same MatchGuid comes back
	// (reconnect), tracking resumes on it instead of starting from scratch.
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
	scorer    *statsapi.PlayerRef
	assister  *statsapi.PlayerRef
	team      int  // -1 unknown
	confirmed bool // team confirmed by a team score change in UpdateState
	speed     float64
}

type rawFeed struct {
	name string
	ref  statsapi.PlayerRef
}

type rawHit struct {
	ref   statsapi.PlayerRef
	speed float64
	has   bool
}

type match struct {
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
	lastSample    time.Time

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

// progressed reports whether game time has started to elapse.
func (m *match) progressed() bool {
	return m.elapsed() > 0 || len(m.goals) > 0
}

// partial reports whether tracking started mid-match (tracker started or
// restarted during the match, or joined in progress).
func (m *match) partial() bool {
	return !m.sawStart || m.firstClock < 0 || (m.firstClock > 0 && m.firstClock < DefaultRegulation)
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

func playerKey(p *statsapi.Player) string {
	if p.PrimaryID != "" {
		return string(p.PrimaryID) + "/" + string(p.Name)
	}
	return string(p.Name) + "#" + strconv.Itoa(int(p.TeamNum)) + "#" + strconv.Itoa(int(p.Shortcut))
}

// resolve finds the player referenced by Name+Shortcut+TeamNum.
func (m *match) resolve(ref *statsapi.PlayerRef) *pstate {
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

// resolveMe applies the identity precedence. source is "id", "name" or "auto".
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
func (t *Tracker) HandleEvent(ev statsapi.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			t.o.Log.Error("tracker: recovered panic", "event", ev.Name, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
		}
	}()
	now := t.o.Now()
	switch ev.Name {
	case statsapi.EvMatchCreated, statsapi.EvMatchInitialized:
		var d statsapi.MatchGUIDOnly
		_ = ev.Decode(&d)
		t.onMatchStart(string(d.MatchGUID), now)
	case statsapi.EvUpdateState:
		var d statsapi.UpdateState
		if err := ev.Decode(&d); err != nil {
			t.o.Log.Debug("bad UpdateState", "err", err)
			return
		}
		t.onUpdate(&d, now)
	case statsapi.EvClockUpdatedSeconds:
		var d statsapi.ClockUpdated
		_ = ev.Decode(&d)
		if t.cur != nil && d.TimeSeconds != nil && t.sameMatch(string(d.MatchGUID)) {
			t.cur.setClock(int(*d.TimeSeconds), bool(d.Overtime), now)
		}
	case statsapi.EvCountdownBegin:
		if t.cur != nil {
			if !t.cur.haveTime {
				t.cur.sawStart = true
			}
			t.cur.roundSeen = true
			t.cur.inRound = false
			t.cur.kickoffSinceGoal = true
		}
	case statsapi.EvRoundStarted:
		if t.cur != nil {
			if !t.cur.haveTime {
				t.cur.sawStart = true
			}
			t.cur.roundSeen = true
			t.cur.inRound = true
			t.cur.replay = false
			t.cur.kickoffSinceGoal = true
		}
	case statsapi.EvGoalReplayStart:
		if t.cur != nil {
			t.cur.replay = true
		}
	case statsapi.EvGoalReplayEnd:
		if t.cur != nil {
			t.cur.replay = false
		}
	case statsapi.EvMatchPaused:
		if t.cur != nil {
			t.cur.paused = true
		}
	case statsapi.EvMatchUnpaused:
		if t.cur != nil {
			t.cur.paused = false
		}
	case statsapi.EvGoalScored:
		var d statsapi.GoalScored
		_ = ev.Decode(&d)
		t.onGoal(&d)
	case statsapi.EvStatfeedEvent:
		var d statsapi.StatfeedEvent
		_ = ev.Decode(&d)
		if t.cur != nil && d.EventName != "" && d.MainTarget.Valid() && t.sameMatch(string(d.MatchGUID)) {
			t.cur.feeds = append(t.cur.feeds, rawFeed{name: string(d.EventName), ref: *d.MainTarget})
		}
	case statsapi.EvBallHit:
		var d statsapi.BallHit
		_ = ev.Decode(&d)
		t.onBallHit(&d)
	case statsapi.EvMatchEnded:
		var d statsapi.MatchEnded
		_ = ev.Decode(&d)
		t.onMatchEnded(&d, now)
	case statsapi.EvMatchDestroyed:
		var d statsapi.MatchGUIDOnly
		_ = ev.Decode(&d)
		guid := string(d.MatchGUID)
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
		case guid != "" && t.cur.rawGUID == "" && !t.cur.progressed():
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
	if r := t.resumable; r != nil && guid != "" && guid == r.rawGUID {
		t.resumable = nil
		r.resume()
		t.cur = r
		t.o.Log.Info("match resumed (reconnected)", "guid", r.guid)
		return
	}
	t.resumable = nil
	t.cur = newMatch(guid, now)
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

func (t *Tracker) onUpdate(d *statsapi.UpdateState, now time.Time) {
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
		case t.cur.rawGUID == "" && guid != "":
			t.cur.adoptGUID(guid)
		case t.cur.rawGUID != "" && guid == "":
			// Transient empty guid during an online match: keep it.
		default:
			t.finish("abandoned", nil, now, "guid changed")
			t.start(guid, now)
		}
	}
	m := t.cur

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

	// Movement sampling (only for players with spectator fields).
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
			if !sp.HasSpectatorFields() {
				continue
			}
			if p := m.players[playerKey(sp)]; p != nil {
				p.mv.add(sp, dt)
			}
		}
	}
}

func (t *Tracker) onGoal(d *statsapi.GoalScored) {
	m := t.cur
	if m == nil || !t.sameMatch(string(d.MatchGUID)) {
		return
	}
	if m.replay {
		return // the game re-sends GoalScored while replaying a goal
	}
	g := rawGoal{t: m.elapsed(), overtime: m.overtime, team: -1, speed: float64(d.GoalSpeed)}
	if d.Scorer.Valid() {
		s := *d.Scorer
		g.scorer = &s
		g.team = int(s.TeamNum)
	} else if d.BallLastTouch != nil && d.BallLastTouch.Player.Valid() {
		g.team = int(d.BallLastTouch.Player.TeamNum)
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

func (t *Tracker) onBallHit(d *statsapi.BallHit) {
	m := t.cur
	if m == nil || !t.sameMatch(string(d.MatchGUID)) {
		return
	}
	if m.replay {
		return // goal replays replay the touches: do not count them twice
	}
	refs := []statsapi.PlayerRef(d.Players)
	if len(refs) == 0 && d.Player.Valid() {
		refs = append(refs, *d.Player)
	}
	for _, r := range refs {
		if r.Name == "" {
			continue
		}
		h := rawHit{ref: r}
		if d.Ball != nil {
			h.speed, h.has = float64(d.Ball.PostHitSpeed), true
		}
		m.hits = append(m.hits, h)
	}
	if len(m.hits) > 100000 { // absurd stream: keep memory bounded
		m.hits = m.hits[len(m.hits)-50000:]
	}
}

func (t *Tracker) onMatchEnded(d *statsapi.MatchEnded, now time.Time) {
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
func (t *Tracker) Flush() { t.Disconnected() }

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
	res, reason := t.build(m, kind, winner, now)
	if res == nil {
		t.o.Log.Info("match not saved", "guid", m.guid, "why", why, "reason", reason)
		return
	}
	if t.o.Save != nil {
		if err := t.o.Save(res); err != nil {
			t.o.Log.Error("saving match failed", "guid", res.GUID, "err", err)
			return
		}
	}
	t.o.Log.Info("match saved", "id", res.ID, "guid", res.GUID, "mode", res.Mode, "result", res.Result,
		"score", fmt.Sprintf("%d-%d", res.TeamScore, res.OppScore), "forfeit", res.Forfeit, "why", why)
}

// Build is exposed for tests: it computes the match record without saving.
func (t *Tracker) build(m *match, kind string, winner *int, now time.Time) (*store.Match, string) {
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
	out := &store.Match{
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
		out.Players = append(out.Players, store.Player{
			Name: p.name, PrimaryID: p.primaryID, Team: p.team, Score: p.score, Goals: p.goals,
			Shots: p.shots, Assists: p.assists, Saves: p.saves, Touches: p.touches, Demos: p.demos, IsMe: isMe,
		})
		if p.team == myTeam && p.score > maxMyTeamScore {
			maxMyTeamScore = p.score
		}
	}
	out.Me = store.MeStats{Name: me.name, PrimaryID: me.primaryID, Score: me.score, Goals: me.goals, Shots: me.shots,
		Assists: me.assists, Saves: me.saves, Touches: me.touches, Demos: me.demos}

	out.Movement = me.mv.result()
	if out.Movement != nil {
		out.Movement.AvgSpeed = round1(out.Movement.AvgSpeed * speedFactor(me.mv.maxSpeed, maxCarKmh))
	}

	// Hits.
	var hc, hsc int
	var hsum, hmax float64
	for _, h := range m.hits {
		if m.resolve(&h.ref) != me {
			continue
		}
		hc++
		if h.has {
			hsc++
			hsum += h.speed
			if h.speed > hmax {
				hmax = h.speed
			}
		}
	}
	if hc > 0 {
		k := speedFactor(hmax, maxBallKmh)
		out.Hits = &store.Hits{Count: hc, MaxSpeed: round1(hmax * k)}
		if hsc > 0 {
			out.Hits.AvgSpeed = round1(hsum / float64(hsc) * k)
		}
	}

	// Goals.
	gmax := 0.0
	for _, g := range m.goals {
		gmax = max(gmax, g.speed)
	}
	gk := speedFactor(gmax, maxBallKmh)
	for _, g := range m.goals {
		sg := store.Goal{T: g.t, Speed: round1(g.speed * gk), Overtime: g.overtime}
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
		out.Goals = append(out.Goals, sg)
	}

	// Statfeed.
	out.Statfeed = map[string]int{}
	for _, f := range m.feeds {
		if m.resolve(&f.ref) == me {
			out.Statfeed[f.name]++
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

// sameRef reports whether two optional player references are the same player.
func sameRef(a, b *statsapi.PlayerRef) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Name == b.Name && a.TeamNum == b.TeamNum
}

// The Stats API documents speeds in Unreal units/s, but the game sends km/h.
// Stored speeds are uu/s (the JSON contract), so the unit is detected per
// match from the largest value: a car tops out at ~83 km/h (2300 uu/s) and
// ball hits/goals stay far below 250 km/h, while in uu/s they exceed these.
const (
	uuPerKmh   = 1 / 0.036
	maxCarKmh  = 120.0
	maxBallKmh = 250.0
)

// speedFactor returns the factor converting values whose largest is peak to uu/s.
func speedFactor(peak, kmhLimit float64) float64 {
	if peak > 0 && peak <= kmhLimit {
		return uuPerKmh
	}
	return 1
}

// Live is the in-match snapshot exposed in /api/status.
type Live struct {
	GUID        string  `json:"guid"`
	Mode        string  `json:"mode"`
	Arena       string  `json:"arena"`
	TimeSeconds int     `json:"time_seconds"`
	Overtime    bool    `json:"overtime"`
	TeamScore   int     `json:"team_score"`
	OppScore    int     `json:"opp_score"`
	MyTeam      int     `json:"my_team"`
	Me          *LiveMe `json:"me"`
}

type LiveMe struct {
	Name    string `json:"name"`
	Score   int    `json:"score"`
	Goals   int    `json:"goals"`
	Shots   int    `json:"shots"`
	Assists int    `json:"assists"`
	Saves   int    `json:"saves"`
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
	l := &Live{GUID: m.guid, Mode: fmt.Sprintf("%dv%d", ts, ts), Arena: m.arena, TimeSeconds: m.timeSeconds, Overtime: m.overtime}
	me, _ := m.resolveMe(t.o.Identity())
	if me != nil {
		l.MyTeam = me.team
		l.Me = &LiveMe{Name: me.name, Score: me.score, Goals: me.goals, Shots: me.shots, Assists: me.assists, Saves: me.saves}
	}
	for tn, s := range m.teamScores {
		if tn == l.MyTeam {
			l.TeamScore = s
		} else {
			l.OppScore += s
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
