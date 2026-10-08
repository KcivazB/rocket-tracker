// Package sim provides a fake Rocket League Stats API server (raw TCP or
// WebSocket) emitting realistic matches, plus a fake match generator for
// seeding a database.
package sim

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"rocket-tracker/internal/statsapi"
)

// Options for the simulator.
type Options struct {
	Port    int     // default 49123
	Mode    string  // "ws" or "tcp"
	Matches int     // number of matches, default 5
	Speed   float64 // game-time multiplier, default 1
	Seed    uint64  // 0 => random
	MeName  string  // default "SimPlayer"
	MeID    string  // default "Epic|sim-me-0001|0"
	Log     *slog.Logger
	Out     io.Writer // human-readable progress (may be nil)
	// UpdatesPerSecond is the number of UpdateState per game second (default 4).
	UpdatesPerSecond int
	// Kinds forces the match kinds in order (cycled): normal, forfeit,
	// abandoned, offline. Empty = random.
	Kinds []string
}

// Server is a running simulator.
type Server struct {
	o   Options
	ln  net.Listener
	rng *rand.Rand

	mu      sync.Mutex
	clients map[*client]struct{}
	joined  chan struct{}
}

type client struct {
	conn net.Conn
	ws   bool
	mu   sync.Mutex
	dead bool
}

// Run starts the simulator and blocks until all matches were played (or ctx ends).
func Run(ctx context.Context, o Options) error {
	if o.Port == 0 {
		o.Port = 49123
	}
	o.Mode = strings.ToLower(o.Mode)
	if o.Mode != "ws" {
		o.Mode = "tcp"
	}
	if o.Matches <= 0 {
		o.Matches = 5
	}
	if o.Speed <= 0 {
		o.Speed = 1
	}
	if o.MeName == "" {
		o.MeName = "SimPlayer"
	}
	if o.MeID == "" {
		o.MeID = "Epic|sim-me-0001|0"
	}
	if o.UpdatesPerSecond <= 0 {
		o.UpdatesPerSecond = 4
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	seed := o.Seed
	if seed == 0 {
		seed = uint64(time.Now().UnixNano())
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(o.Port)))
	if err != nil {
		return err
	}
	s := &Server{o: o, ln: ln, rng: rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15)), clients: map[*client]struct{}{}, joined: make(chan struct{}, 1)}
	defer ln.Close()
	go s.acceptLoop()
	fmt.Fprintf(o.Out, "simulator listening on 127.0.0.1:%d (mode=%s, matches=%d, speed=%gx)\n", o.Port, o.Mode, o.Matches, o.Speed)

	for i := 0; i < o.Matches; i++ {
		if err := s.waitClient(ctx); err != nil {
			return err
		}
		kind := s.pickKind()
		if len(o.Kinds) > 0 {
			kind = matchKind(o.Kinds[i%len(o.Kinds)])
		}
		fmt.Fprintf(o.Out, "match %d/%d starting (%s)\n", i+1, o.Matches, kind)
		summary, err := s.playMatch(ctx, kind)
		if err != nil {
			return err
		}
		fmt.Fprintf(o.Out, "match %d/%d done: %s\n", i+1, o.Matches, summary)
		if !s.sleepGame(ctx, 3) {
			return ctx.Err()
		}
	}
	fmt.Fprintln(o.Out, "all matches sent; closing in 2s")
	select {
	case <-time.After(2 * time.Second):
	case <-ctx.Done():
	}
	s.mu.Lock()
	for c := range s.clients {
		c.conn.Close()
	}
	s.mu.Unlock()
	return nil
}

func (s *Server) acceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *Server) handle(conn net.Conn) {
	c := &client{conn: conn}
	br := bufio.NewReader(conn)
	if s.o.Mode == "ws" {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		req, err := http.ReadRequest(br)
		if err != nil || !strings.EqualFold(req.Header.Get("Upgrade"), "websocket") {
			_, _ = io.WriteString(conn, "HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\n\r\n")
			conn.Close()
			return
		}
		_ = conn.SetReadDeadline(time.Time{})
		key := req.Header.Get("Sec-WebSocket-Key")
		_, err = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: "+statsapi.ComputeAccept(key)+"\r\n\r\n")
		if err != nil {
			conn.Close()
			return
		}
		c.ws = true
	}
	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.mu.Unlock()
	select {
	case s.joined <- struct{}{}:
	default:
	}
	fmt.Fprintf(s.o.Out, "client connected: %s\n", conn.RemoteAddr())
	// Read (and mostly discard) whatever the client sends.
	if c.ws {
		wr := &statsapi.WSReader{R: br, W: writerFunc(func(p []byte) (int, error) { c.mu.Lock(); defer c.mu.Unlock(); return conn.Write(p) })}
		for {
			if _, err := wr.ReadMessage(); err != nil {
				break
			}
		}
	} else {
		_, _ = io.Copy(io.Discard, br)
	}
	s.drop(c)
	fmt.Fprintf(s.o.Out, "client disconnected: %s\n", conn.RemoteAddr())
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func (s *Server) drop(c *client) {
	s.mu.Lock()
	delete(s.clients, c)
	s.mu.Unlock()
	c.conn.Close()
}

func (s *Server) waitClient(ctx context.Context) error {
	for {
		s.mu.Lock()
		n := len(s.clients)
		s.mu.Unlock()
		if n > 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.joined:
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// emit broadcasts one event. Data is randomly sent as an object or as a
// JSON-encoded string; TCP objects are sometimes glued together.
func (s *Server) emit(name string, data any) {
	db, _ := json.Marshal(data)
	var env []byte
	if s.rng.Float64() < 0.3 {
		str, _ := json.Marshal(string(db))
		env = []byte(`{"Event":` + strconv.Quote(name) + `,"Data":` + string(str) + `}`)
	} else {
		env = []byte(`{"Event":` + strconv.Quote(name) + `,"Data":` + string(db) + `}`)
	}
	sep := s.rng.IntN(3) // 0: none, 1: \n, 2: \r\n
	s.mu.Lock()
	cl := make([]*client, 0, len(s.clients))
	for c := range s.clients {
		cl = append(cl, c)
	}
	s.mu.Unlock()
	for _, c := range cl {
		var err error
		c.mu.Lock()
		_ = c.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		if c.ws {
			err = statsapi.WriteFrame(c.conn, statsapi.OpText, env, false)
		} else {
			b := env
			switch sep {
			case 1:
				b = append(append([]byte{}, env...), '\n')
			case 2:
				b = append(append([]byte{}, env...), '\r', '\n')
			}
			_, err = c.conn.Write(b)
		}
		c.mu.Unlock()
		if err != nil {
			go s.drop(c)
		}
	}
}

func (s *Server) sleepGame(ctx context.Context, gameSeconds float64) bool {
	d := time.Duration(gameSeconds / s.o.Speed * float64(time.Second))
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// ---- match simulation ----

type matchKind string

const (
	kindNormal    matchKind = "normal"
	kindForfeit   matchKind = "forfeit"
	kindAbandoned matchKind = "abandoned"
	kindOffline   matchKind = "offline"
)

func (s *Server) pickKind() matchKind {
	x := s.rng.Float64()
	switch {
	case x < 0.12:
		return kindForfeit
	case x < 0.22:
		return kindAbandoned
	case x < 0.30:
		return kindOffline
	}
	return kindNormal
}

type simPlayer struct {
	Name     string
	ID       string
	Shortcut int
	Team     int
	Score    int
	Goals    int
	Shots    int
	Assists  int
	Saves    int
	Touches  int
	Demos    int
	skill    float64

	boost      float64
	speed      float64
	onGround   bool
	onWall     bool
	demolished float64 // seconds left
}

var (
	arenas = []string{"Stadium_P", "EuroStadium_Night_P", "Park_P", "TrainStation_P", "UtopiaStadium_P", "Wasteland_P",
		"NeoTokyo_Standard_P", "beach_P", "cs_p", "Underwater_P", "Stadium_Foggy_P", "ChinaTown_P", "Farm_P"}
	namePool = []string{"Kaiser", "Zephyr", "Mikachu", "Drakkar", "Nova", "Tipsy", "Gizmo", "Raptor", "Lumen", "Orion",
		"Pixel", "Bolt", "Vortex", "Saber", "Echo", "Juno", "Blaze", "Frost", "Comet", "Rook", "Atlas", "Nyx", "Kodiak",
		"Banshee", "Turbo", "Wasp", "Moose", "Falcon", "Yeti", "Pulse"}
	platforms = []string{"Epic", "Steam", "PS4", "XboxOne", "Switch"}
)

type simMatch struct {
	s        *Server
	guid     string
	arena    string
	players  []*simPlayer
	me       *simPlayer
	scores   [2]int
	time     int
	overtime bool
	replay   bool
	frame    int
	elapsed  float64
}

func (s *Server) newMatch(kind matchKind) *simMatch {
	r := s.rng
	size := 2
	switch x := r.Float64(); {
	case x < 0.25:
		size = 1
	case x < 0.7:
		size = 2
	default:
		size = 3
	}
	m := &simMatch{s: s, time: 300}
	if kind != kindOffline {
		m.guid = fmt.Sprintf("%08X%08X%08X%08X", r.Uint32(), r.Uint32(), r.Uint32(), r.Uint32())
	}
	m.arena = arenas[r.IntN(len(arenas))]
	if r.Float64() < 0.05 {
		m.arena = "HoopsStadium_P"
	} else if r.Float64() < 0.04 {
		m.arena = "ShatterShot_P"
	}
	myTeam := r.IntN(2)
	perm := r.Perm(len(namePool))
	sc := 1
	for team := 0; team < 2; team++ {
		for i := 0; i < size; i++ {
			p := &simPlayer{Shortcut: sc, Team: team, skill: 0.7 + r.Float64()*0.6, boost: 33, onGround: true}
			sc++
			if team == myTeam && i == 0 {
				p.Name, p.ID = s.o.MeName, s.o.MeID
				p.skill = 1.05
				m.me = p
			} else {
				p.Name = namePool[perm[len(m.players)%len(perm)]]
				if kind == kindOffline {
					p.Name = "Bot " + p.Name
					p.ID = "Unknown|0|0"
				} else {
					p.ID = fmt.Sprintf("%s|%d|0", platforms[r.IntN(len(platforms))], 1000000+r.IntN(9000000))
				}
			}
			m.players = append(m.players, p)
		}
	}
	return m
}

type refJSON struct {
	Name     string `json:"Name"`
	Shortcut int    `json:"Shortcut"`
	TeamNum  int    `json:"TeamNum"`
}

func (p *simPlayer) ref() refJSON { return refJSON{p.Name, p.Shortcut, p.Team} }

func (m *simMatch) state() map[string]any {
	players := make([]map[string]any, 0, len(m.players))
	for _, p := range m.players {
		pj := map[string]any{
			"Name": p.Name, "PrimaryId": p.ID, "Shortcut": p.Shortcut, "TeamNum": p.Team,
			"Score": p.Score, "Goals": p.Goals, "Shots": p.Shots, "Assists": p.Assists, "Saves": p.Saves,
			"Touches": p.Touches, "CarTouches": p.Touches, "Demos": p.Demos,
		}
		if p.Team == m.me.Team { // spectator fields only for own team
			pj["bHasCar"] = p.demolished <= 0
			pj["Speed"] = math.Round(p.speed)
			pj["Boost"] = math.Round(p.boost)
			pj["bBoosting"] = p.boost > 0 && m.s.rng.Float64() < 0.2
			pj["bOnGround"] = p.onGround
			pj["bOnWall"] = p.onWall
			pj["bPowersliding"] = p.onGround && m.s.rng.Float64() < 0.04
			pj["bDemolished"] = p.demolished > 0
			pj["bSupersonic"] = p.speed >= 2200
		}
		players = append(players, pj)
	}
	teams := []map[string]any{
		{"Name": "Blue", "TeamNum": 0, "Score": m.scores[0], "ColorPrimary": "1873FF", "ColorSecondary": "E5E5E5"},
		{"Name": "Orange", "TeamNum": 1, "Score": m.scores[1], "ColorPrimary": "C26418", "ColorSecondary": "E5E5E5"},
	}
	game := map[string]any{
		"Teams": teams, "TimeSeconds": m.time, "bOvertime": m.overtime, "Frame": m.frame, "Elapsed": m.elapsed,
		"Ball":    map[string]any{"Speed": m.s.rng.Float64() * 3000, "TeamNum": m.s.rng.IntN(2)},
		"bReplay": m.replay, "bHasWinner": false, "Winner": "", "Arena": m.arena,
		"bHasTarget": !m.replay, "Target": m.me.ref(),
	}
	if m.replay {
		delete(game, "Target")
	}
	return map[string]any{"MatchGuid": m.guid, "Players": players, "Game": game}
}

func (m *simMatch) jiggle() {
	r := m.s.rng
	for _, p := range m.players {
		if p.demolished > 0 {
			continue
		}
		p.speed = math.Max(0, math.Min(2300, p.speed+r.NormFloat64()*500))
		if r.Float64() < 0.1 {
			p.speed = 2200 + r.Float64()*100
		}
		p.boost = math.Max(0, math.Min(100, p.boost+r.NormFloat64()*15))
		if r.Float64() < 0.03 {
			p.boost = 100
		}
		if r.Float64() < 0.03 {
			p.boost = 0
		}
		x := r.Float64()
		p.onGround, p.onWall = x < 0.62, x >= 0.62 && x < 0.72
	}
}

// tickSecond sends the updates for one game second.
func (m *simMatch) tickSecond(ctx context.Context) bool {
	n := m.s.o.UpdatesPerSecond
	for i := 0; i < n; i++ {
		m.jiggle()
		m.frame += 120 / n
		m.elapsed += 1.0 / float64(n)
		m.s.emit(statsapi.EvUpdateState, m.state())
		if !m.s.sleepGame(ctx, 1.0/float64(n)) {
			return false
		}
	}
	for _, p := range m.players {
		if p.demolished > 0 {
			p.demolished--
		}
	}
	return true
}

func (m *simMatch) clock() {
	m.s.emit(statsapi.EvClockUpdatedSeconds, map[string]any{"TimeSeconds": m.time, "bOvertime": m.overtime, "MatchGuid": m.guid})
}

func (m *simMatch) feed(name string, main *simPlayer, sec *simPlayer) {
	d := map[string]any{"EventName": name, "Type": name, "MainTarget": main.ref(), "MatchGuid": m.guid}
	if sec != nil {
		d["SecondaryTarget"] = sec.ref()
	}
	m.s.emit(statsapi.EvStatfeedEvent, d)
}

func (m *simMatch) pick(team int) *simPlayer {
	var c []*simPlayer
	total := 0.0
	for _, p := range m.players {
		if p.Team == team {
			c = append(c, p)
			total += p.skill
		}
	}
	x := m.s.rng.Float64() * total
	for _, p := range c {
		x -= p.skill
		if x <= 0 {
			return p
		}
	}
	return c[len(c)-1]
}

func (m *simMatch) teamSkill(team int) float64 {
	t := 0.0
	for _, p := range m.players {
		if p.Team == team {
			t += p.skill
		}
	}
	return t
}

// randomEvents produces hits, shots, saves, demos and maybe a goal. Returns the scoring team or -1.
func (m *simMatch) randomEvents() int {
	r := m.s.rng
	if r.Float64() < 0.7 {
		p := m.players[r.IntN(len(m.players))]
		p.Touches++
		pre := r.Float64() * 2000
		m.s.emit(statsapi.EvBallHit, map[string]any{"MatchGuid": m.guid, "Players": []refJSON{p.ref()},
			"Ball": map[string]any{"PreHitSpeed": pre, "PostHitSpeed": pre + 300 + r.Float64()*2500,
				"Location": map[string]any{"X": r.Float64()*8000 - 4000, "Y": r.Float64()*10000 - 5000, "Z": r.Float64() * 1500}}})
	}
	if r.Float64() < 0.012 {
		a := m.players[r.IntN(len(m.players))]
		v := m.players[r.IntN(len(m.players))]
		if a.Team != v.Team && v.demolished <= 0 {
			a.Demos++
			a.Score += 10
			v.demolished = 3
			m.feed("Demolish", a, v)
		}
	}
	size := float64(len(m.players)) / 2
	for team := 0; team < 2; team++ {
		rate := 0.0105 * m.teamSkill(team) / size
		if m.overtime {
			rate *= 1.4
		}
		if r.Float64() < rate*2.5 { // a shot
			sh := m.pick(team)
			sh.Shots++
			sh.Score += 20
			m.feed("Shot", sh, nil)
			if r.Float64() < 0.4 {
				return team // goal
			}
			sv := m.pick(1 - team)
			sv.Saves++
			sv.Score += 50
			if r.Float64() < 0.15 {
				sv.Score += 25
				m.feed("EpicSave", sv, nil)
			} else {
				m.feed("Save", sv, nil)
			}
		}
	}
	return -1
}

func (m *simMatch) goal(ctx context.Context, team int) bool {
	r := m.s.rng
	scorer := m.pick(team)
	var assister *simPlayer
	if len(m.players) > 2 && r.Float64() < 0.55 {
		for i := 0; i < 5; i++ {
			a := m.pick(team)
			if a != scorer {
				assister = a
				break
			}
		}
	}
	m.scores[team]++
	scorer.Goals++
	scorer.Score += 100
	speed := 1500 + r.Float64()*3000
	d := map[string]any{"GoalSpeed": speed, "GoalTime": float64(300 - m.time), "MatchGuid": m.guid,
		"ImpactLocation": map[string]any{"X": r.Float64()*1700 - 850, "Y": 5120 * float64(1-2*team), "Z": r.Float64() * 600},
		"Scorer":         scorer.ref(), "BallLastTouch": map[string]any{"Player": scorer.ref(), "Speed": speed}}
	if assister != nil {
		d["Assister"] = assister.ref()
		assister.Assists++
		assister.Score += 50
	}
	m.s.emit(statsapi.EvGoalScored, d)
	m.feed("Goal", scorer, nil)
	if r.Float64() < 0.2 {
		m.feed("AerialGoal", scorer, nil)
	}
	if speed > 4000 {
		m.feed("LongGoal", scorer, nil)
	}
	if scorer.Goals == 3 {
		m.feed("HatTrick", scorer, nil)
	}
	if assister != nil {
		m.feed("Assist", assister, nil)
	}
	if m.overtime {
		m.feed("OvertimeGoal", scorer, nil)
		return true
	}
	// Replay with frozen clock.
	m.s.emit(statsapi.EvGoalReplayStart, map[string]any{"MatchGuid": m.guid})
	m.replay = true
	for i := 0; i < 3; i++ {
		if !m.tickSecond(ctx) {
			return false
		}
	}
	m.s.emit(statsapi.EvGoalReplayWillEnd, map[string]any{"MatchGuid": m.guid})
	m.replay = false
	m.s.emit(statsapi.EvGoalReplayEnd, map[string]any{"MatchGuid": m.guid})
	m.s.emit(statsapi.EvCountdownBegin, map[string]any{"MatchGuid": m.guid})
	for i := 0; i < 2; i++ {
		if !m.tickSecond(ctx) {
			return false
		}
	}
	m.s.emit(statsapi.EvRoundStarted, map[string]any{"MatchGuid": m.guid})
	return true
}

func (s *Server) playMatch(ctx context.Context, kind matchKind) (string, error) {
	m := s.newMatch(kind)
	r := s.rng
	s.emit(statsapi.EvMatchCreated, map[string]any{"MatchGuid": m.guid})
	s.emit(statsapi.EvMatchInitialized, map[string]any{"MatchGuid": m.guid})
	s.emit(statsapi.EvCountdownBegin, map[string]any{"MatchGuid": m.guid})
	for i := 0; i < 3; i++ {
		if !m.tickSecond(ctx) {
			return "", ctx.Err()
		}
	}
	s.emit(statsapi.EvRoundStarted, map[string]any{"MatchGuid": m.guid})

	stopAt := -1 // regulation TimeSeconds at which we forfeit/abandon
	switch kind {
	case kindForfeit:
		stopAt = 40 + r.IntN(150)
	case kindAbandoned:
		stopAt = 60 + r.IntN(180)
	}
	pauseAt := -1
	if r.Float64() < 0.1 {
		pauseAt = 100 + r.IntN(100)
	}

	ended := false
	for !ended {
		if !m.tickSecond(ctx) {
			return "", ctx.Err()
		}
		if m.overtime {
			m.time++
		} else if m.time > 0 {
			m.time--
		}
		m.clock()
		if m.time == pauseAt && !m.overtime {
			s.emit(statsapi.EvMatchPaused, map[string]any{"MatchGuid": m.guid})
			for i := 0; i < 4; i++ {
				m.s.emit(statsapi.EvUpdateState, m.state())
				s.sleepGame(ctx, 1)
			}
			s.emit(statsapi.EvMatchUnpaused, map[string]any{"MatchGuid": m.guid})
		}
		if team := m.randomEvents(); team >= 0 {
			if !m.goal(ctx, team) {
				return "", ctx.Err()
			}
			if m.overtime {
				ended = true
				break
			}
		}
		if !m.overtime && m.time == stopAt {
			if kind == kindAbandoned {
				s.emit(statsapi.EvMatchDestroyed, map[string]any{"MatchGuid": m.guid})
				return fmt.Sprintf("abandoned at %ds left, %d-%d", m.time, m.scores[0], m.scores[1]), nil
			}
			// Forfeit: the trailing team gives up (if tied, the opponent of me).
			ended = true
			break
		}
		if !m.overtime && m.time == 0 {
			if m.scores[0] == m.scores[1] {
				m.overtime = true
				s.emit(statsapi.EvCountdownBegin, map[string]any{"MatchGuid": m.guid})
				m.tickSecond(ctx)
				s.emit(statsapi.EvRoundStarted, map[string]any{"MatchGuid": m.guid})
			} else {
				ended = true
			}
		}
		if m.overtime && m.time > 600 { // safety: force a goal
			if !m.goal(ctx, r.IntN(2)) {
				return "", ctx.Err()
			}
			ended = true
		}
	}
	winner := 0
	switch {
	case m.scores[1] > m.scores[0]:
		winner = 1
	case m.scores[1] == m.scores[0]:
		winner = m.me.Team // tied forfeit: the opponents give up
	}
	// A couple of final states (goal celebration) before the end.
	for i := 0; i < 2; i++ {
		s.emit(statsapi.EvUpdateState, m.state())
		s.sleepGame(ctx, 0.5)
	}
	// Winners get the Win feed, best winner MVP.
	var mvp *simPlayer
	for _, p := range m.players {
		if p.Team == winner {
			m.feed("Win", p, nil)
			if mvp == nil || p.Score > mvp.Score {
				mvp = p
			}
		}
	}
	if mvp != nil {
		m.feed("MVP", mvp, nil)
	}
	s.emit(statsapi.EvMatchEnded, map[string]any{"WinnerTeamNum": winner, "MatchGuid": m.guid})
	s.emit(statsapi.EvPodiumStart, map[string]any{"MatchGuid": m.guid})
	for i := 0; i < 3; i++ {
		s.emit(statsapi.EvUpdateState, m.state())
		s.sleepGame(ctx, 1)
	}
	s.emit(statsapi.EvMatchDestroyed, map[string]any{"MatchGuid": m.guid})
	res := "loss"
	if winner == m.me.Team {
		res = "win"
	}
	extra := ""
	if kind == kindForfeit {
		extra = fmt.Sprintf(" forfeit at %ds left", m.time)
	}
	if m.overtime {
		extra += fmt.Sprintf(" overtime %ds", m.time)
	}
	mode := fmt.Sprintf("%dv%d", len(m.players)/2, len(m.players)/2)
	online := "online"
	if m.guid == "" {
		online = "offline"
	}
	return fmt.Sprintf("%s %s %s me=%s blue %d - %d orange%s", online, mode, res, teamName(m.me.Team), m.scores[0], m.scores[1], extra), nil
}

func teamName(t int) string {
	if t == 0 {
		return "blue"
	}
	return "orange"
}
