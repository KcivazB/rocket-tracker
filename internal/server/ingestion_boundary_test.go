package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rocket-tracker/internal/store"
)

func ingestionFixture() *store.Match {
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	return &store.Match{GUID: "synthetic", Online: true, StartedAt: start,
		EndedAt: start.Add(5 * time.Minute), DurationS: 300, Result: "loss",
		TeamScore: 1, OppScore: 2, Me: store.MeStats{Name: "Synthetic", PrimaryID: "synthetic-profile"}}
}

// Exercise authentication and persistence through the real handler, without a socket.
func ingestionHandler(t *testing.T) (http.Handler, *store.Scope, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "ingestion.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	u, err := st.UpsertOIDCUser(context.Background(), "synthetic:alice", "alice", "Alice", "")
	if err != nil {
		t.Fatal(err)
	}
	sc := st.User(u.ID)
	_, token, err := sc.CreateDevice(context.Background(), "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: st, Hub: &Hub{PublicURL: "https://ingestion.invalid"}}
	return s.Handler(), sc, token
}

func TestIngestionRejectsNegativeQuantities(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*store.Match)
	}{
		{"duration", func(m *store.Match) { m.DurationS = -1 }},
		{"overtime", func(m *store.Match) { m.OvertimeS = -1 }},
		{"team score", func(m *store.Match) { m.TeamScore = -1 }},
		{"opponent score", func(m *store.Match) { m.OppScore = -1 }},
		{"player score", func(m *store.Match) { m.Me.Score = -1 }},
		{"goals", func(m *store.Match) { m.Me.Goals = -1 }},
		{"shots", func(m *store.Match) { m.Me.Shots = -1 }},
		{"assists", func(m *store.Match) { m.Me.Assists = -1 }},
		{"saves", func(m *store.Match) { m.Me.Saves = -1 }},
		{"touches", func(m *store.Match) { m.Me.Touches = -1 }},
		{"demos", func(m *store.Match) { m.Me.Demos = -1 }},
		{"other player", func(m *store.Match) { m.Players = []store.Player{{Goals: -1}} }},
		{"movement", func(m *store.Match) { m.Movement = &store.Movement{SampleS: -1} }},
		{"hits", func(m *store.Match) { m.Hits = &store.Hits{Count: -1} }},
		{"hit speed", func(m *store.Match) { m.Hits = &store.Hits{MaxSpeed: -1} }},
		{"statfeed", func(m *store.Match) { m.Statfeed = map[string]int{"Shot": -1} }},
		{"regulation", func(m *store.Match) { m.RegulationS = -180 }},
		{"event time", func(m *store.Match) { m.Events = []store.Event{{T: -1, Kind: "shot", Team: "us"}} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, sc, token := ingestionHandler(t)
			m := ingestionFixture()
			tc.mutate(m)
			body, err := json.Marshal(map[string]any{"key": "invalid", "match": m})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodPost, "https://ingestion.invalid/api/agent/matches", bytes.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest {
				t.Errorf("upload status %d, want 400", w.Code)
			}
			res, err := (&Importer{Scope: sc}).QueueEntry(context.Background(), "invalid.json", body)
			if err != nil || res.Matches != 0 || len(res.Skipped) != 1 {
				t.Errorf("import result %+v, err %v", res, err)
			}
			if n, err := sc.Count(context.Background()); err != nil || n != 0 {
				t.Fatalf("invalid upload/import persisted %d matches: %v", n, err)
			}
		})
	}
}

func TestIngestionPreservesPartialAndSignedTelemetry(t *testing.T) {
	_, sc, _ := ingestionHandler(t)
	m := ingestionFixture()
	m.Partial = true
	m.DurationS = 0 // older records may not carry a measured duration
	m.Goals = []store.Goal{{T: 20, Team: "them", Impact: &store.Vec{X: -20, Y: -5120}}}
	m.Hits = &store.Hits{Points: []store.HitPoint{{X: -10, Y: -1000}}}
	if err := IngestAgentMatch(context.Background(), sc, "ranked", "old-partial", m); err != nil {
		t.Fatal(err)
	}
	ms, err := sc.List(context.Background())
	if err != nil || len(ms) != 1 {
		t.Fatalf("matches %d: %v", len(ms), err)
	}
	if ms[0].GoalDiff != -1 || ms[0].Movement != nil || !ms[0].Partial || ms[0].DurationS != 0 || ms[0].Goals[0].Impact.Y != -5120 || ms[0].Hits.Points[0].Y != -1000 {
		t.Fatalf("partial or signed data changed: %+v", ms[0])
	}
}

func TestDatabaseImportSkipsInvalidMatchQuantities(t *testing.T) {
	ctx := context.Background()
	srcPath := filepath.Join(t.TempDir(), "source.db")
	src, err := store.Open(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	bad := ingestionFixture()
	bad.Me.Goals = -1
	good := ingestionFixture()
	good.GUID = "valid"
	if err := src.SaveMany(ctx, []*store.Match{bad, good}); err != nil {
		src.Close()
		t.Fatal(err)
	}
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}
	_, sc, _ := ingestionHandler(t)
	im := &Importer{Scope: sc}
	for range 2 {
		res, err := im.Database(ctx, srcPath)
		if err != nil || res.Matches != 1 || len(res.Skipped) != 1 {
			t.Fatalf("import %+v: %v", res, err)
		}
	}
	if n, err := sc.Count(ctx); err != nil || n != 1 {
		t.Fatalf("valid rows after replay: %d: %v", n, err)
	}
	ms, err := sc.List(ctx)
	if err != nil || len(ms) != 1 || ms[0].GUID != "valid" {
		t.Fatalf("wrong survivor after import: %+v: %v", ms, err)
	}
}

func TestAgentJSONEnvelope(t *testing.T) {
	for _, training := range []bool{false, true} {
		path, limit, field := "/api/agent/matches", int64(MaxMatchBody), "match"
		value := any(ingestionFixture())
		if training {
			path, limit, field = "/api/agent/trainings", MaxTrainingBody, "training"
			start := ingestionFixture().StartedAt
			value = &store.Training{StartedAt: start, EndedAt: start.Add(time.Minute), Source: "game"}
		}
		prefix, err := json.Marshal(map[string]any{"key": "envelope", field: value, "future_field": true})
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name, suffix  string
			status        int
			invalidPrefix bool
		}{
			{"normal", "\n\t ", 200, false},
			{"second-value", "{}", 400, false},
			{"invalid-suffix", "x", 400, false},
			{"exact-limit", strings.Repeat(" ", int(limit)-len(prefix)), 200, false},
			{"oversize", strings.Repeat(" ", int(limit)-len(prefix)+1), 413, false},
			{"early-error-oversize", strings.Repeat(" ", int(limit)), 413, true},
			{"second-value-oversize", "{}" + strings.Repeat(" ", int(limit)), 413, false},
			{"invalid-suffix-oversize", "x" + strings.Repeat(" ", int(limit)), 413, false},
		} {
			for _, unknown := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/unknown-length=%v", field, tc.name, unknown), func(t *testing.T) {
					h, sc, token := ingestionHandler(t)
					bodyPrefix := prefix
					if tc.invalidPrefix {
						bodyPrefix = []byte("x")
					}
					r := httptest.NewRequest(http.MethodPost, "https://ingestion.invalid"+path, io.MultiReader(bytes.NewReader(bodyPrefix), strings.NewReader(tc.suffix)))
					if unknown {
						r.ContentLength = -1
					} else {
						r.ContentLength = int64(len(bodyPrefix) + len(tc.suffix))
					}
					r.Header.Set("Authorization", "Bearer "+token)
					w := httptest.NewRecorder()
					h.ServeHTTP(w, r)
					if w.Code != tc.status {
						t.Fatalf("status %d, want %d: %s", w.Code, tc.status, w.Body.String())
					}
					matches, err := sc.Count(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					trainings, err := sc.Trainings(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					want := 0
					if tc.status == 200 {
						want = 1
					}
					if matches+len(trainings) != want {
						t.Fatalf("persisted %d rows, want %d", matches+len(trainings), want)
					}
				})
			}
		}
	}
}
