package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"rocket-tracker/internal/config"
	"rocket-tracker/internal/sim"
	"rocket-tracker/internal/store"
)

func newTestServer(t *testing.T) (*httptest.Server, *store.Store) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg, err := config.Load(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	ms := sim.Generate(sim.SeedOptions{N: 25, Seed: 7})
	if err := st.SaveMany(context.Background(), ms); err != nil {
		t.Fatal(err)
	}
	s := &Server{Version: "test", Store: st, Config: cfg, Static: fstest.MapFS{"index.html": {Data: []byte("<h1>dash</h1>")}, "embed.go": {Data: []byte("package web")}}}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

func do(t *testing.T, method, url, body string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func TestAPI(t *testing.T) {
	ts, _ := newTestServer(t)

	resp, body := do(t, "GET", ts.URL+"/api/status", "")
	var st map[string]any
	if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &st) != nil {
		t.Fatalf("status %d %s", resp.StatusCode, body)
	}
	if st["match_count"].(float64) != 25 || st["live"] != nil || st["connected"] != false {
		t.Fatalf("status %s", body)
	}

	resp, body = do(t, "GET", ts.URL+"/api/matches", "")
	var ms []map[string]any
	if err := json.Unmarshal([]byte(body), &ms); err != nil || len(ms) != 25 {
		t.Fatalf("matches %v %d", err, len(ms))
	}
	for _, k := range []string{"id", "guid", "online", "started_at", "ended_at", "duration_s", "overtime", "overtime_s", "arena", "mode",
		"team_size", "variant", "tag", "result", "forfeit", "my_team", "team_score", "opp_score", "goal_diff", "first_goal", "mvp",
		"me", "players", "movement", "hits", "goals", "statfeed"} {
		if _, ok := ms[0][k]; !ok {
			t.Errorf("missing key %s", k)
		}
	}
	id := int(ms[0]["id"].(float64))
	url := ts.URL + "/api/matches/" + itoa(id)

	resp, body = do(t, "PATCH", url, `{"tag":"Casual"}`)
	if resp.StatusCode != 200 || !strings.Contains(body, `"tag":"casual"`) {
		t.Fatalf("patch %d %s", resp.StatusCode, body)
	}
	if resp, _ = do(t, "PATCH", url, `{"tag":"nope"}`); resp.StatusCode != 400 {
		t.Fatalf("bad tag => %d", resp.StatusCode)
	}
	if resp, _ = do(t, "DELETE", url, ""); resp.StatusCode != 204 {
		t.Fatalf("delete %d", resp.StatusCode)
	}
	if resp, _ = do(t, "DELETE", url, ""); resp.StatusCode != 404 {
		t.Fatalf("delete again %d", resp.StatusCode)
	}

	resp, body = do(t, "GET", ts.URL+"/api/export.csv", "")
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if resp.StatusCode != 200 || len(lines) != 25 || !strings.HasPrefix(lines[0], "\ufeffid;guid;online") {
		t.Fatalf("csv %d lines %d: %q", resp.StatusCode, len(lines), lines[0])
	}
	resp, body = do(t, "GET", ts.URL+"/api/export.csv?sep=,", "")
	if lines = strings.Split(strings.TrimSpace(body), "\n"); resp.StatusCode != 200 || len(lines) != 25 || !strings.HasPrefix(lines[0], "\ufeffid,guid,online") {
		t.Fatalf("csv comma %d %q", resp.StatusCode, lines[0])
	}

	// Regression: an invalid default_tag silently became "ranked".
	if resp, body = do(t, "PUT", ts.URL+"/api/config", `{"default_tag":"bogus"}`); resp.StatusCode != 400 {
		t.Fatalf("invalid default_tag: %d %s", resp.StatusCode, body)
	}

	resp, body = do(t, "PUT", ts.URL+"/api/config", `{"player_names":["Me"," me ",""],"default_tag":"casual"}`)
	if resp.StatusCode != 200 || !strings.Contains(body, `"player_names":["Me"]`) || !strings.Contains(body, `"dashboard_port":8765`) {
		t.Fatalf("put config %d %s", resp.StatusCode, body)
	}
	if _, body = do(t, "GET", ts.URL+"/api/config", ""); !strings.Contains(body, `"default_tag":"casual"`) {
		t.Fatalf("get config %s", body)
	}

	if _, body = do(t, "GET", ts.URL+"/", ""); body != "<h1>dash</h1>" {
		t.Fatalf("index %q", body)
	}
	if resp, _ = do(t, "GET", ts.URL+"/embed.go", ""); resp.StatusCode != 404 {
		t.Fatalf("embed.go must not be served: %d", resp.StatusCode)
	}

	// DNS-rebinding guard.
	req, _ := http.NewRequest("GET", ts.URL+"/api/status", nil)
	req.Host = "evil.example"
	r2, err := http.DefaultClient.Do(req)
	if err != nil || r2.StatusCode != 403 {
		t.Fatalf("foreign host => %v %v", r2.StatusCode, err)
	}
}

func itoa(i int) string { return jsonNum(i) }

func jsonNum(i int) string { b, _ := json.Marshal(i); return string(b) }

// Regression: player names (chosen by other players) were exported raw, so a
// name like "=HYPERLINK(...)" became a live formula in Excel.
func TestCSVFormulaInjection(t *testing.T) {
	m := &store.Match{MyTeam: 0, TeamScore: 1, OppScore: 2, GoalDiff: -1,
		Me:       store.MeStats{Name: "@me"},
		Players:  []store.Player{{Name: "@me", Team: 0, IsMe: true}, {Name: "=cmd|' /C calc'!A0", Team: 1}, {Name: "+mate", Team: 0}},
		Statfeed: map[string]int{}}
	row := CSVRow(m)
	col := func(name string) string {
		for i, h := range CSVHeader {
			if h == name {
				return row[i]
			}
		}
		t.Fatalf("no column %s", name)
		return ""
	}
	if col("opponents") != "'=cmd|' /C calc'!A0" || col("teammates") != "'+mate" || col("me_name") != "'@me" {
		t.Fatalf("not neutralized: %q %q %q", col("opponents"), col("teammates"), col("me_name"))
	}
	if col("goal_diff") != "-1" {
		t.Fatalf("numeric cell altered: %q", col("goal_diff"))
	}
}

func TestHostGuard(t *testing.T) {
	ts, _ := newTestServer(t)
	for host, want := range map[string]int{"localhost:8765": 200, "127.0.0.1:8765": 200, "LOCALHOST": 200, "[::1]:8765": 200, "evil.example:8765": 403} {
		req, _ := http.NewRequest("GET", ts.URL+"/api/status", nil)
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("host %s: %d, want %d", host, resp.StatusCode, want)
		}
	}
}

func TestCSVDecimalCommaAndUnknownGoals(t *testing.T) {
	m := &store.Match{DurationS: 312.5, Goals: []store.Goal{{Team: "us"}, {Team: ""}, {Team: "them"}}, Statfeed: map[string]int{}}
	row := csvRow(m, true)
	if row[5] != "312,5" || CSVRow(m)[5] != "312.5" {
		t.Fatalf("duration cells %q / %q", row[5], CSVRow(m)[5])
	}
	// Regression: a goal with unknown team was counted in goals_them.
	n := len(CSVHeader)
	if CSVHeader[n-4] != "goals_us" || row[n-4] != "1" || row[n-3] != "1" {
		t.Fatalf("goals_us/them %q %q", row[n-4], row[n-3])
	}
}

func TestPutConfigGoalPartial(t *testing.T) {
	ts, _ := newTestServer(t)
	resp, body := do(t, "PUT", ts.URL+"/api/config", `{"goal":{"season_start":"2026-09-01","season_end":"2026-12-09"}}`)
	if resp.StatusCode != 200 {
		t.Fatalf("put goal %d %s", resp.StatusCode, body)
	}
	var c config.Config
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		t.Fatal(err)
	}
	// Fields absent from the body keep their current values.
	if c.Goal != (config.Goal{Mode: "1v1", Daily: 10, SeasonStart: "2026-09-01", SeasonEnd: "2026-12-09"}) || c.DefaultTag != "ranked" {
		t.Fatalf("goal merge: %+v", c)
	}
}

func TestManualAPI(t *testing.T) {
	ts, _ := newTestServer(t)
	if resp, body := do(t, "GET", ts.URL+"/api/manual", ""); resp.StatusCode != 200 || strings.TrimSpace(body) != "[]" {
		t.Fatalf("empty list %d %q", resp.StatusCode, body)
	}
	for _, c := range []struct{ path, body string }{
		{"/api/manual/2026-10-08/1v1", `{"games":3,"wins":4}`},
		{"/api/manual/2026-10-08/5v5", `{"games":3,"wins":1}`},
		{"/api/manual/not-a-day/1v1", `{"games":3,"wins":1}`},
		{"/api/manual/2026-10-08/1v1", `{"wins":1}`},
		{"/api/manual/2026-10-08/1v1", `nope`},
	} {
		if resp, body := do(t, "PUT", ts.URL+c.path, c.body); resp.StatusCode != 400 {
			t.Fatalf("PUT %s %s: want 400, got %d %s", c.path, c.body, resp.StatusCode, body)
		}
	}
	if resp, body := do(t, "PUT", ts.URL+"/api/manual/2026-10-08/1V1", `{"games":7,"wins":4}`); resp.StatusCode != 200 {
		t.Fatalf("put %d %s", resp.StatusCode, body)
	}
	if _, body := do(t, "GET", ts.URL+"/api/manual", ""); !strings.Contains(body, `{"day":"2026-10-08","mode":"1v1","games":7,"wins":4}`) {
		t.Fatalf("list %s", body)
	}
	if resp, _ := do(t, "PUT", ts.URL+"/api/manual/2026-10-08/1v1", `{"games":0}`); resp.StatusCode != 200 {
		t.Fatalf("delete %d", resp.StatusCode)
	}
	if _, body := do(t, "GET", ts.URL+"/api/manual", ""); strings.TrimSpace(body) != "[]" {
		t.Fatalf("after delete %s", body)
	}
}

func TestInstallUpdate(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	installed := 0
	restarted := make(chan struct{}, 1)
	s := &Server{Version: "0.5.0", Store: st,
		InstallUpdate: func() error { installed++; return nil },
		Restart:       func() { restarted <- struct{}{} }}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	post := func(origin string) int {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/update/install", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := post("https://evil.example"); code != http.StatusForbidden || installed != 0 {
		t.Fatalf("cross-origin: %d, installed %d", code, installed)
	}
	if code := post("http://localhost:8765"); code != http.StatusOK || installed != 1 {
		t.Fatalf("local: %d, installed %d", code, installed)
	}
	<-restarted
}

func TestRanksAPI(t *testing.T) {
	ts, _ := newTestServer(t)
	resp, body := do(t, "POST", ts.URL+"/api/ranks", `{"playlist":"2v2","tier":14,"division":3,"mmr":1032}`)
	if resp.StatusCode != 201 || !strings.Contains(body, `"tier":14`) || !strings.Contains(body, `"mmr":1032`) {
		t.Fatalf("add %d %s", resp.StatusCode, body)
	}
	if resp, body := do(t, "POST", ts.URL+"/api/ranks", `{"playlist":"1v1","mmr":845}`); resp.StatusCode != 201 || !strings.Contains(body, `"tier":null`) {
		t.Fatalf("mmr only %d %s", resp.StatusCode, body)
	}
	if resp, _ := do(t, "POST", ts.URL+"/api/ranks", `{"playlist":"1v1"}`); resp.StatusCode != 400 {
		t.Fatalf("empty entry accepted: %d", resp.StatusCode)
	}
	resp, body = do(t, "GET", ts.URL+"/api/ranks", "")
	var rs []struct {
		ID int64 `json:"id"`
	}
	if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &rs) != nil || len(rs) != 2 {
		t.Fatalf("list %d %s", resp.StatusCode, body)
	}
	if resp, _ := do(t, "DELETE", fmt.Sprintf("%s/api/ranks/%d", ts.URL, rs[0].ID), ""); resp.StatusCode != 204 {
		t.Fatalf("delete %d", resp.StatusCode)
	}
	if resp, _ := do(t, "DELETE", fmt.Sprintf("%s/api/ranks/%d", ts.URL, rs[0].ID), ""); resp.StatusCode != 404 {
		t.Fatalf("delete twice %d", resp.StatusCode)
	}
}
