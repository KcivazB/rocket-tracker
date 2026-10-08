// Package server exposes the HTTP API and the embedded dashboard.
package server

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"rocket-tracker/internal/config"
	"rocket-tracker/internal/setup"
	"rocket-tracker/internal/store"
	"rocket-tracker/internal/tracker"
)

// Server wires the HTTP API.
type Server struct {
	Version    string
	Store      *store.Store
	Config     *config.Manager
	Tracker    *tracker.Tracker      // may be nil (no live data)
	ConnStatus func() (bool, string) // may be nil
	Static     fs.FS                 // may be nil
	Log        *slog.Logger

	iniMu   sync.Mutex
	iniAt   time.Time
	iniDir  string
	iniStat setup.IniStatus
}

// Handler returns the root handler.
func (s *Server) Handler() http.Handler {
	if s.Log == nil {
		s.Log = slog.New(slog.DiscardHandler)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /api/matches", s.listMatches)
	mux.HandleFunc("GET /api/matches/{id}", s.getMatch)
	mux.HandleFunc("PATCH /api/matches/{id}", s.patchMatch)
	mux.HandleFunc("DELETE /api/matches/{id}", s.deleteMatch)
	mux.HandleFunc("GET /api/export.csv", s.exportCSV)
	mux.HandleFunc("GET /api/config", s.getConfig)
	mux.HandleFunc("PUT /api/config", s.putConfig)
	mux.HandleFunc("GET /api/manual", s.listManual)
	mux.HandleFunc("PUT /api/manual/{day}/{mode}", s.putManual)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "unknown endpoint")
	})
	mux.Handle("/", s.staticHandler())
	return s.guard(mux)
}

// guard rejects non-local Host headers (DNS rebinding) and recovers panics.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.Log.Error("http panic", "path", r.URL.Path, "panic", fmt.Sprint(rec))
				writeErr(w, http.StatusInternalServerError, "internal error")
			}
		}()
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.Trim(strings.ToLower(host), "[]")
		if host != "localhost" && host != "127.0.0.1" && host != "::1" && host != "" {
			writeErr(w, http.StatusForbidden, "forbidden host")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

type identityJSON struct {
	Names []string `json:"names"`
	IDs   []string `json:"ids"`
}

type statusJSON struct {
	Version    string          `json:"version"`
	Connected  bool            `json:"connected"`
	Transport  string          `json:"transport"`
	InMatch    bool            `json:"in_match"`
	Live       *tracker.Live   `json:"live"`
	Ini        setup.IniStatus `json:"ini"`
	Identity   identityJSON    `json:"identity"`
	MatchCount int             `json:"match_count"`
}

func (s *Server) ini(cfg config.Config) setup.IniStatus {
	s.iniMu.Lock()
	defer s.iniMu.Unlock()
	if time.Since(s.iniAt) < 15*time.Second && s.iniDir == cfg.RLInstallDir {
		return s.iniStat
	}
	dir := setup.FindInstallDir(cfg.RLInstallDir)
	s.iniStat = setup.CheckIni(dir)
	s.iniAt, s.iniDir = time.Now(), cfg.RLInstallDir
	return s.iniStat
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	cfg := s.Config.Get()
	st := statusJSON{Version: s.Version, Ini: s.ini(cfg), Identity: identityJSON{Names: cfg.PlayerNames, IDs: cfg.PlayerIDs}}
	if s.ConnStatus != nil {
		st.Connected, st.Transport = s.ConnStatus()
	}
	if s.Tracker != nil {
		st.Live = s.Tracker.Live()
		st.InMatch = st.Live != nil
	}
	if n, err := s.Store.Count(r.Context()); err == nil {
		st.MatchCount = n
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) listMatches(w http.ResponseWriter, r *http.Request) {
	ms, err := s.Store.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ms)
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func (s *Server) getMatch(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	m, err := s.Store.Get(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	} else if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) patchMatch(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	var body struct {
		Tag *string `json:"tag"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Tag == nil {
		writeErr(w, http.StatusBadRequest, "missing tag")
		return
	}
	tag := strings.ToLower(strings.TrimSpace(*body.Tag))
	if !store.ValidTag(tag) {
		writeErr(w, http.StatusBadRequest, "tag must be one of ranked, casual, tournament, private, other")
		return
	}
	m, err := s.Store.SetTag(r.Context(), id, tag)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	} else if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) deleteMatch(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	err := s.Store.Delete(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	} else if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listManual(w http.ResponseWriter, r *http.Request) {
	ds, err := s.Store.ListManual(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ds)
}

// putManual sets the hand-entered games of one day and mode; games 0 removes the entry.
func (s *Server) putManual(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Games *int `json:"games"`
		Wins  int  `json:"wins"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Games == nil {
		writeErr(w, http.StatusBadRequest, "missing games")
		return
	}
	d := store.ManualDay{Day: r.PathValue("day"), Mode: strings.ToLower(r.PathValue("mode")), Games: *body.Games, Wins: body.Wins}
	err := s.Store.SetManual(r.Context(), d)
	if errors.Is(err, store.ErrInvalidManual) {
		writeErr(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "store: invalid manual entry: "))
		return
	} else if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Config.Get())
}

func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	c := s.Config.Get() // partial bodies keep the other fields
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&c); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if !store.ValidTag(strings.TrimSpace(c.DefaultTag)) {
		writeErr(w, http.StatusBadRequest, "default_tag must be one of ranked, casual, tournament, private, other")
		return
	}
	saved, err := s.Config.Set(c)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.iniMu.Lock()
	s.iniAt = time.Time{}
	s.iniMu.Unlock()
	writeJSON(w, http.StatusOK, saved)
}

// CSVHeader is the flat export column list.
var CSVHeader = []string{
	"id", "guid", "online", "started_at", "ended_at", "duration_s", "overtime", "overtime_s", "arena", "mode",
	"team_size", "variant", "tag", "result", "forfeit", "my_team", "team_score", "opp_score", "goal_diff",
	"first_goal", "mvp", "me_name", "me_primary_id", "me_score", "me_goals", "me_shots", "me_assists", "me_saves",
	"me_touches", "me_demos", "teammates", "opponents",
	"mv_sample_s", "mv_avg_speed", "mv_supersonic_pct", "mv_ground_pct", "mv_wall_pct", "mv_air_pct",
	"mv_avg_boost", "mv_zero_boost_pct", "mv_full_boost_pct", "mv_boosting_pct", "mv_powerslide_pct",
	"mv_demolished_s", "hits_count", "hits_avg_speed", "hits_max_speed", "goals_us", "goals_them", "statfeed",
	"partial",
}

func (s *Server) exportCSV(w http.ResponseWriter, r *http.Request) {
	ms, err := s.Store.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Default: ';' with decimal commas, what a French-locale Excel opens
	// directly. ?sep=, gives a standard comma-separated file (decimal points).
	sep := ';'
	if v := r.URL.Query().Get("sep"); v == "," || strings.EqualFold(v, "comma") {
		sep = ','
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="rocket-tracker-matches.csv"`)
	_, _ = io.WriteString(w, "\xef\xbb\xbf") // BOM so Excel detects UTF-8
	cw := csv.NewWriter(w)
	cw.Comma = sep
	_ = cw.Write(CSVHeader)
	for _, m := range ms {
		_ = cw.Write(csvRow(m, sep == ';'))
	}
	cw.Flush()
}

// csvText neutralizes spreadsheet formula injection for free-text cells
// (player names are chosen by other players): a cell starting with = + - @
// or a control character is prefixed with a quote so Excel shows it as text.
func csvText(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r\n", rune(s[0])) {
		return "'" + s
	}
	return s
}

// CSVRow flattens a match (decimal points).
func CSVRow(m *store.Match) []string { return csvRow(m, false) }

func csvRow(m *store.Match, decimalComma bool) []string {
	b := strconv.FormatBool
	i := strconv.Itoa
	f := func(x float64) string {
		s := strconv.FormatFloat(x, 'f', -1, 64)
		if decimalComma {
			s = strings.Replace(s, ".", ",", 1)
		}
		return s
	}
	var mates, opps []string
	for _, p := range m.Players {
		if p.IsMe {
			continue
		}
		if p.Team == m.MyTeam {
			mates = append(mates, p.Name)
		} else {
			opps = append(opps, p.Name)
		}
	}
	gu, gt := 0, 0
	for _, g := range m.Goals {
		switch g.Team {
		case "us":
			gu++
		case "them":
			gt++
		}
	}
	keys := make([]string, 0, len(m.Statfeed))
	for k := range m.Statfeed {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sf []string
	for _, k := range keys {
		sf = append(sf, k+"="+i(m.Statfeed[k]))
	}
	row := []string{
		strconv.FormatInt(m.ID, 10), csvText(m.GUID), b(m.Online), m.StartedAt.Format(time.RFC3339), m.EndedAt.Format(time.RFC3339),
		f(m.DurationS), b(m.Overtime), f(m.OvertimeS), csvText(m.Arena), m.Mode, i(m.TeamSize), m.Variant, m.Tag, m.Result,
		b(m.Forfeit), i(m.MyTeam), i(m.TeamScore), i(m.OppScore), i(m.GoalDiff), m.FirstGoal, b(m.MVP),
		csvText(m.Me.Name), csvText(m.Me.PrimaryID), i(m.Me.Score), i(m.Me.Goals), i(m.Me.Shots), i(m.Me.Assists), i(m.Me.Saves),
		i(m.Me.Touches), i(m.Me.Demos), csvText(strings.Join(mates, ";")), csvText(strings.Join(opps, ";")),
	}
	if mv := m.Movement; mv != nil {
		row = append(row, f(mv.SampleS), f(mv.AvgSpeed), f(mv.SupersonicPct), f(mv.GroundPct), f(mv.WallPct),
			f(mv.AirPct), f(mv.AvgBoost), f(mv.ZeroBoostPct), f(mv.FullBoostPct), f(mv.BoostingPct),
			f(mv.PowerslidePct), f(mv.DemolishedS))
	} else {
		row = append(row, make([]string, 12)...)
	}
	if h := m.Hits; h != nil {
		row = append(row, i(h.Count), f(h.AvgSpeed), f(h.MaxSpeed))
	} else {
		row = append(row, "", "", "")
	}
	row = append(row, i(gu), i(gt), csvText(strings.Join(sf, ";")), b(m.Partial))
	return row
}

const fallbackIndex = `<!doctype html><html lang="fr"><head><meta charset="utf-8"><title>Rocket Tracker</title>
<style>body{background:#0b1020;color:#e6e9f2;font-family:system-ui,sans-serif;padding:2rem}a{color:#5cc8ff}</style>
</head><body><h1>Rocket Tracker</h1><p>Le tableau de bord n'est pas inclus dans ce build.</p>
<p>API : <a href="/api/status">/api/status</a> · <a href="/api/matches">/api/matches</a> ·
<a href="/api/export.csv">export CSV</a></p></body></html>`

func (s *Server) staticHandler() http.Handler {
	var fileServer http.Handler
	hasIndex := false
	if s.Static != nil {
		fileServer = http.FileServerFS(s.Static)
		if _, err := fs.Stat(s.Static, "index.html"); err == nil {
			hasIndex = true
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		p := r.URL.Path
		if strings.HasSuffix(p, ".go") {
			http.NotFound(w, r)
			return
		}
		if (p == "/" || p == "/index.html") && !hasIndex {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, fallbackIndex)
			return
		}
		if fileServer == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		fileServer.ServeHTTP(w, r)
	})
}

// ListenAndServe serves on 127.0.0.1:port until ctx is done.
func (s *Server) ListenAndServe(ctx context.Context, port int) error {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	err = srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
