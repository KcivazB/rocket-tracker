package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"rocket-tracker/internal/config"
	"rocket-tracker/internal/setup"
	"rocket-tracker/internal/store"
	"rocket-tracker/internal/tilt"
	"rocket-tracker/internal/tracker"
)

// Hub configures the multi-user server mode: players sign in with OpenID
// Connect, and each one's agents (rltracker agent, on the gaming PCs) send
// their matches and live state with a device token.
type Hub struct {
	PublicURL  string        // external base URL, e.g. https://rl.example.com
	OIDC       *OIDC         // nil when only DevLogin is enabled
	DevLogin   bool          // INSECURE: sign in as anyone (local testing)
	SessionTTL time.Duration // default 30 days
	Discord    *Discord      // nil: no Discord posts

	agents agentRegistry
}

// AgentOnlineFor is how long an agent counts as online after a heartbeat.
const AgentOnlineFor = 15 * time.Second

const sessionCookie = "rt_session"

func (h *Hub) ttl() time.Duration {
	if h.SessionTTL > 0 {
		return h.SessionTTL
	}
	return 30 * 24 * time.Hour
}

func (h *Hub) secureCookies() bool {
	return strings.HasPrefix(strings.ToLower(h.PublicURL), "https://")
}

// origin is the scheme://host[:port] browsers send in the Origin header.
func (h *Hub) origin() string {
	u, err := url.Parse(h.PublicURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Scheme + "://" + u.Host)
}

type ctxKey int

const (
	userKey ctxKey = iota
	deviceKey
)

// userFrom returns the signed-in user (server mode), or nil.
func userFrom(r *http.Request) *store.User {
	u, _ := r.Context().Value(userKey).(*store.User)
	return u
}

func deviceFrom(r *http.Request) *store.Device {
	d, _ := r.Context().Value(deviceKey).(*store.Device)
	return d
}

// authenticate resolves who sends the request. It returns the request with
// the user in its context, or nil after answering (401, redirect to login).
//   - /auth/* and /healthz are public;
//   - /api/agent/* need a device token (Authorization: Bearer rtk_...);
//   - everything else needs a session cookie, and state-changing requests
//     must come from the dashboard's own origin (CSRF).
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) *http.Request {
	p := r.URL.Path
	if strings.HasPrefix(p, "/auth/") || p == "/healthz" || p == "/style.css" {
		return r // style.css: the sign-in pages use it
	}
	ctx := r.Context()
	if strings.HasPrefix(p, "/api/agent/") {
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		u, d, err := s.Store.DeviceUser(ctx, strings.TrimSpace(tok))
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				s.Log.Error("device auth", "err", err)
			}
			writeErr(w, http.StatusUnauthorized, "invalid or revoked device token")
			return nil
		}
		ctx = context.WithValue(ctx, userKey, u)
		return r.WithContext(context.WithValue(ctx, deviceKey, d))
	}
	var u *store.User
	if c, err := r.Cookie(sessionCookie); err == nil {
		u, err = s.Store.SessionUser(ctx, c.Value, s.Hub.ttl())
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			s.Log.Error("session lookup", "err", err)
		}
	}
	if u == nil {
		if strings.HasPrefix(p, "/api/") {
			writeErr(w, http.StatusUnauthorized, "not signed in")
		} else if r.Method == http.MethodGet || r.Method == http.MethodHead {
			http.Redirect(w, r, "/auth/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
		} else {
			writeErr(w, http.StatusUnauthorized, "not signed in")
		}
		return nil
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && !s.sameOrigin(r) {
		writeErr(w, http.StatusForbidden, "cross-origin request refused")
		return nil
	}
	return r.WithContext(context.WithValue(ctx, userKey, u))
}

// sameOrigin reports whether a browser request comes from the dashboard.
func (s *Server) sameOrigin(r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" {
		return strings.EqualFold(o, s.Hub.origin())
	}
	sf := r.Header.Get("Sec-Fetch-Site")
	return sf == "" || sf == "same-origin"
}

func (s *Server) hubRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok\n") })
	mux.HandleFunc("GET /auth/login", s.login)
	mux.HandleFunc("GET /auth/callback", s.callback)
	mux.HandleFunc("POST /auth/logout", s.logout)
	mux.HandleFunc("GET /auth/logged-out", s.loggedOut)
	if s.Hub.DevLogin {
		mux.HandleFunc("GET /auth/dev", s.devLogin)
	}

	mux.HandleFunc("GET /api/players", s.players)
	mux.HandleFunc("GET /api/players/{handle}/matches", s.listMatches)
	mux.HandleFunc("GET /api/players/{handle}/manual", s.listManual)
	mux.HandleFunc("GET /api/players/{handle}/config", s.playerConfig)
	mux.HandleFunc("GET /api/players/{handle}/status", s.hubStatus)
	mux.HandleFunc("GET /api/players/{handle}/export.csv", s.exportCSV)
	mux.HandleFunc("GET /api/leaderboard", s.leaderboard)
	mux.HandleFunc("GET /api/devices", s.listDevices)
	mux.HandleFunc("POST /api/devices", s.createDevice)
	mux.HandleFunc("DELETE /api/devices/{id}", s.deleteDevice)

	mux.HandleFunc("POST /api/agent/heartbeat", s.agentHeartbeat)
	mux.HandleFunc("POST /api/agent/matches", s.agentMatch)
	mux.HandleFunc("PUT /api/agent/manual/{day}/{mode}", s.putManual)
}

// ---------------------------------------------------------------- settings

// userSettings returns a user's settings (identity, default tag, season goal).
func (s *Server) userSettings(ctx context.Context, uid int64) (config.Config, error) {
	return UserSettings(ctx, s.Store, uid)
}

// UserSettings returns the settings of an account (defaults when unset).
func UserSettings(ctx context.Context, st *store.Store, uid int64) (config.Config, error) {
	c := config.Defaults()
	raw, err := st.UserSettings(ctx, uid)
	if err != nil {
		return c, err
	}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &c) // a broken value falls back to the defaults
	}
	c.Normalize()
	return c, nil
}

func (s *Server) setUserSettings(ctx context.Context, uid int64, c config.Config) (config.Config, error) {
	c.Normalize()
	c.RLInstallDir = "" // lives on the gaming PC (agent), not on the server
	b, err := json.Marshal(c)
	if err != nil {
		return c, err
	}
	return c, s.Store.SetUserSettings(ctx, uid, string(b))
}

// playerConfig is the part of another player's settings their dashboard needs.
func (s *Server) playerConfig(w http.ResponseWriter, r *http.Request) {
	u, err := s.Store.UserByHandle(r.Context(), r.PathValue("handle"))
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "unknown player")
		return
	} else if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	c, err := s.userSettings(r.Context(), u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"goal": c.Goal})
}

// ---------------------------------------------------------------- agents

// agentReport is what an agent sends every few seconds.
type agentReport struct {
	Version      string           `json:"version"`
	Connected    bool             `json:"connected"` // to Rocket League
	Transport    string           `json:"transport"`
	Live         *tracker.Live    `json:"live"`
	Ini          *setup.IniStatus `json:"ini"`
	AutoIdentity *struct {
		Name      string `json:"name"`
		PrimaryID string `json:"primary_id"`
	} `json:"auto_identity,omitempty"`
}

type agentSeen struct {
	rep    agentReport
	device string
	at     time.Time
}

func (a agentSeen) online(now time.Time) bool { return now.Sub(a.at) < AgentOnlineFor }

// agentRegistry keeps the last report of each device (in memory: it is
// rebuilt within seconds after a restart).
type agentRegistry struct {
	mu sync.Mutex
	m  map[int64]map[int64]agentSeen // user id -> device id
}

func (a *agentRegistry) put(uid, did int64, device string, rep agentReport) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.m == nil {
		a.m = map[int64]map[int64]agentSeen{}
	}
	if a.m[uid] == nil {
		a.m[uid] = map[int64]agentSeen{}
	}
	a.m[uid][did] = agentSeen{rep: rep, device: device, at: time.Now()}
}

func (a *agentRegistry) forget(uid, did int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.m[uid], did)
}

// best returns the most relevant agent of a user: one in a match, else one
// connected to the game, else the most recently seen.
func (a *agentRegistry) best(uid int64) (agentSeen, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	rank := func(x agentSeen) int {
		switch {
		case !x.online(now):
			return 0
		case x.rep.Live != nil:
			return 3
		case x.rep.Connected:
			return 2
		}
		return 1
	}
	var (
		out   agentSeen
		found bool
	)
	for _, x := range a.m[uid] {
		if !found || rank(x) > rank(out) || (rank(x) == rank(out) && x.at.After(out.at)) {
			out, found = x, true
		}
	}
	return out, found
}

type agentJSON struct {
	Online   bool   `json:"online"`
	Device   string `json:"device"`
	LastSeen string `json:"last_seen"`
	Version  string `json:"version"`
	Devices  int    `json:"devices"` // registered devices (own status only)
}

// hubStatus is /api/status in server mode: the live state comes from the
// player's agents.
func (s *Server) hubStatus(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	own := r.PathValue("handle") == ""
	st := statusJSON{Version: s.Version, Agent: &agentJSON{}}
	if n, err := sc.Count(ctx); err == nil {
		st.MatchCount = n
	}
	if own {
		if c, err := s.userSettings(ctx, sc.UserID()); err == nil {
			st.Identity = identityJSON{Names: c.PlayerNames, IDs: c.PlayerIDs}
		}
		if ds, err := sc.Devices(ctx); err == nil {
			st.Agent.Devices = len(ds)
		}
	}
	if a, found := s.Hub.agents.best(sc.UserID()); found {
		st.Agent.Device, st.Agent.LastSeen, st.Agent.Version = a.device, a.at.UTC().Format(time.RFC3339), a.rep.Version
		if a.online(time.Now()) {
			st.Agent.Online = true
			st.Connected, st.Transport = a.rep.Connected, a.rep.Transport
			st.Live, st.InMatch = a.rep.Live, a.rep.Live != nil
			if own {
				st.Ini = a.rep.Ini
			}
		}
	}
	writeJSON(w, http.StatusOK, st)
}

type agentSettingsJSON struct {
	PlayerNames []string `json:"player_names"`
	PlayerIDs   []string `json:"player_ids"`
	DefaultTag  string   `json:"default_tag"`
}

// agentHeartbeat records an agent's state and answers with the settings the
// tracker needs (identity, default tag).
func (s *Server) agentHeartbeat(w http.ResponseWriter, r *http.Request) {
	var rep agentReport
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&rep); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	u, d, ctx := userFrom(r), deviceFrom(r), r.Context()
	c, err := s.userSettings(ctx, u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if ai := rep.AutoIdentity; ai != nil && len(c.PlayerNames) == 0 && len(c.PlayerIDs) == 0 && (ai.Name != "" || ai.PrimaryID != "") {
		if ai.Name != "" {
			c.PlayerNames = []string{ai.Name}
		}
		if ai.PrimaryID != "" {
			c.PlayerIDs = []string{ai.PrimaryID}
		}
		if c, err = s.setUserSettings(ctx, u.ID, c); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.Log.Info("auto-detected player identity saved", "user", u.Handle, "name", ai.Name)
	}
	rep.AutoIdentity = nil
	s.Hub.agents.put(u.ID, d.ID, d.Name, rep)
	writeJSON(w, http.StatusOK, map[string]any{
		"user":     map[string]string{"handle": u.Handle, "name": u.Name},
		"device":   d.Name,
		"settings": agentSettingsJSON{PlayerNames: c.PlayerNames, PlayerIDs: c.PlayerIDs, DefaultTag: c.DefaultTag},
	})
}

// MaxMatchBody bounds an uploaded match (a long match with movement data is
// well under 100 kB).
const MaxMatchBody = 4 << 20

// agentMatch stores a finished match sent by an agent. The key makes the
// upload idempotent (retries, imports run twice).
func (s *Server) agentMatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key   string       `json:"key"`
		Match *store.Match `json:"match"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, MaxMatchBody)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	u, m := userFrom(r), body.Match
	c, err := s.userSettings(r.Context(), u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := IngestAgentMatch(r.Context(), s.Store.User(u.ID), c.DefaultTag, body.Key, m); errors.Is(err, ErrInvalidMatch) {
		writeErr(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), ErrInvalidMatch.Error()+": "))
		return
	} else if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Log.Info("match received", "user", u.Handle, "device", deviceFrom(r).Name, "id", m.ID, "mode", m.Mode, "result", m.Result)
	resp := struct {
		ID    int64       `json:"id"`
		Alert *tilt.Alert `json:"alert,omitempty"` // break suggestion, shown by the agent
	}{ID: m.ID}
	if c.TiltStreak > 0 || (s.Hub.Discord != nil && !c.DiscordOff) {
		if ms, err := s.Store.User(u.ID).List(r.Context()); err == nil {
			now := time.Now()
			resp.Alert = tilt.Check(ms, c.TiltStreak, now)
			if s.Hub.Discord != nil && !c.DiscordOff {
				s.Hub.Discord.Match(u, m, ms, now)
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// ErrInvalidMatch wraps the reasons a match sent by an agent is refused.
var ErrInvalidMatch = errors.New("invalid match")

// IngestAgentMatch checks a match sent by an agent (or imported from a gaming
// PC's files) and stores it in sc, with defaultTag when it has no valid tag.
// key identifies the agent's copy: the same key again updates instead of
// duplicating.
func IngestAgentMatch(ctx context.Context, sc *store.Scope, defaultTag, key string, m *store.Match) error {
	switch {
	case m == nil:
		return fmt.Errorf("%w: missing match", ErrInvalidMatch)
	case len(key) > 200:
		return fmt.Errorf("%w: key too long", ErrInvalidMatch)
	case m.Result != "win" && m.Result != "loss" && m.Result != "abandoned":
		return fmt.Errorf("%w: result must be win, loss or abandoned", ErrInvalidMatch)
	case m.StartedAt.IsZero() || m.EndedAt.Before(m.StartedAt):
		return fmt.Errorf("%w: invalid match times", ErrInvalidMatch)
	}
	m.ID = 0
	m.Tag = strings.ToLower(strings.TrimSpace(m.Tag))
	if !store.ValidTag(m.Tag) {
		m.Tag = defaultTag
	}
	return sc.Ingest(ctx, m, key)
}

// ---------------------------------------------------------------- devices

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	ds, err := s.Store.User(userFrom(r).ID).Devices(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	type deviceJSON struct {
		store.Device
		Online bool `json:"online"`
	}
	out := make([]deviceJSON, 0, len(ds))
	s.Hub.agents.mu.Lock()
	seen := s.Hub.agents.m[userFrom(r).ID]
	for _, d := range ds {
		a, ok := seen[d.ID]
		out = append(out, deviceJSON{Device: d, Online: ok && a.online(time.Now())})
	}
	s.Hub.agents.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

// createDevice returns the new token once; only its hash is kept.
func (s *Server) createDevice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	d, tok, err := s.Store.User(userFrom(r).ID).CreateDevice(r.Context(), body.Name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"device": d, "token": tok, "server": strings.TrimRight(s.Hub.PublicURL, "/")})
}

func (s *Server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	uid := userFrom(r).ID
	err := s.Store.User(uid).DeleteDevice(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	} else if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Hub.agents.forget(uid, id)
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- players

type playerJSON struct {
	Handle     string        `json:"handle"`
	Name       string        `json:"name"`
	IsMe       bool          `json:"is_me"`
	Matches    int           `json:"matches"`
	LastPlayed string        `json:"last_played"`
	GameNames  []string      `json:"game_names"`
	Online     bool          `json:"online"` // agent running
	InMatch    bool          `json:"in_match"`
	Live       *tracker.Live `json:"live"`
}

// players lists every account with a short summary (search is client-side).
func (s *Server) players(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	us, err := s.Store.Users(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	sums, err := s.Store.PlayerSummaries(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	me := userFrom(r)
	now := time.Now()
	out := make([]playerJSON, 0, len(us))
	for _, u := range us {
		p := playerJSON{Handle: u.Handle, Name: u.Name, IsMe: u.ID == me.ID, GameNames: []string{}}
		if sm := sums[u.ID]; sm != nil {
			p.Matches, p.LastPlayed, p.GameNames = sm.Matches, sm.LastPlayed, sm.GameNames
		}
		if c, err := s.userSettings(ctx, u.ID); err == nil {
			p.GameNames = mergeNames(c.PlayerNames, p.GameNames)
		}
		if a, ok := s.Hub.agents.best(u.ID); ok && a.online(now) {
			p.Online, p.Live, p.InMatch = true, a.rep.Live, a.rep.Live != nil
		}
		out = append(out, p)
	}
	writeJSON(w, http.StatusOK, out)
}

func mergeNames(lists ...[]string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, l := range lists {
		for _, n := range l {
			if k := strings.ToLower(n); n != "" && !seen[k] {
				seen[k] = true
				out = append(out, n)
			}
		}
	}
	return out
}

type leaderJSON struct {
	Handle string `json:"handle"`
	Name   string `json:"name"`
	IsMe   bool   `json:"is_me"`
	Games  int    `json:"games"` // decided (won + lost)
	store.LeaderRow
	Winrate *float64 `json:"winrate"` // 0..1, null without decided games
}

// leaderboard ranks the players by win rate over the selected matches
// (?mode=all|1v1|2v2|3v3|other &tag=all|ranked|... &days=7|30|90|all).
func (s *Server) leaderboard(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lq := store.LeaderboardQuery{Mode: q.Get("mode"), Tag: q.Get("tag")}
	switch lq.Mode {
	case "", "all", "1v1", "2v2", "3v3", "other":
	default:
		writeErr(w, http.StatusBadRequest, "mode must be all, 1v1, 2v2, 3v3 or other")
		return
	}
	if lq.Tag != "" && lq.Tag != "all" && !store.ValidTag(lq.Tag) {
		writeErr(w, http.StatusBadRequest, "unknown tag")
		return
	}
	if d := q.Get("days"); d != "" && d != "all" {
		n, err := strconv.Atoi(d)
		if err != nil || n <= 0 || n > 3650 {
			writeErr(w, http.StatusBadRequest, "days must be a number of days or all")
			return
		}
		lq.Since = time.Now().AddDate(0, 0, -n)
	}
	ctx := r.Context()
	rows, err := s.Store.Leaderboard(ctx, lq)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	me := userFrom(r)
	out := make([]leaderJSON, 0, len(rows))
	for _, row := range rows {
		u, err := s.Store.UserByID(ctx, row.UserID)
		if err != nil {
			continue
		}
		l := leaderJSON{Handle: u.Handle, Name: u.Name, IsMe: u.ID == me.ID, Games: row.Wins + row.Losses, LeaderRow: row}
		if l.Games > 0 {
			wr := float64(row.Wins) / float64(l.Games)
			l.Winrate = &wr
		}
		out = append(out, l)
	}
	sort.SliceStable(out, func(i, j int) bool {
		wi, wj := -1.0, -1.0
		if out[i].Winrate != nil {
			wi = *out[i].Winrate
		}
		if out[j].Winrate != nil {
			wj = *out[j].Winrate
		}
		if wi != wj {
			return wi > wj
		}
		return out[i].Games > out[j].Games
	})
	writeJSON(w, http.StatusOK, out)
}
