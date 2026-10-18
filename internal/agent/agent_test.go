package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"rocket-tracker/internal/server"
	"rocket-tracker/internal/sim"
	"rocket-tracker/internal/store"
)

// testServer is a real rltracker-server (dev login) with one user and one
// device; down makes it answer 503 like a stopped reverse proxy.
func testServer(t *testing.T) (*httptest.Server, *store.Store, *store.User, string, *atomic.Bool) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	u, _ := st.UpsertOIDCUser(ctx, "sub", "virgile", "Virgile", "")
	_, tok, _ := st.User(u.ID).CreateDevice(ctx, "PC")
	var down atomic.Bool
	srv := &server.Server{Version: "test", Store: st, Hub: &server.Hub{PublicURL: "http://x", DevLogin: true}}
	h := srv.Handler()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts, st, u, tok, &down
}

func TestOutboxSurvivesOutageAndDedupes(t *testing.T) {
	ts, st, u, tok, down := testServer(t)
	ob, err := OpenOutbox(filepath.Join(t.TempDir(), "outbox"))
	if err != nil {
		t.Fatal(err)
	}
	a := New(&Config{Server: ts.URL, Token: tok}, Options{Client: &Client{Server: ts.URL, Token: tok}, Outbox: ob})
	ctx := context.Background()
	ms := sim.Generate(sim.SeedOptions{N: 3, Seed: 3})
	for _, m := range ms {
		m.Online = true
		if err := a.Save(m); err != nil {
			t.Fatal(err)
		}
	}
	bad := *ms[0]
	bad.Result = "draw" // the server refuses it
	bad.GUID = "bad"
	_ = ob.Put("zzz-bad", &bad)

	down.Store(true)
	if n, err := a.uploadPending(ctx); err == nil || n != 0 {
		t.Fatalf("upload while down: %d %v", n, err)
	}
	if p, _ := ob.Pending(); len(p) != 4 {
		t.Fatalf("pending after outage %d", len(p))
	}
	down.Store(false)
	if n, err := a.uploadPending(ctx); err != nil || n != 3 {
		t.Fatalf("upload: %d %v", n, err)
	}
	if p, _ := ob.Pending(); len(p) != 0 {
		t.Fatalf("pending after upload %v", p)
	}
	if _, err := os.Stat(filepath.Join(ob.Dir(), "rejected", "zzz-bad.json")); err != nil {
		t.Fatal("rejected match not set aside")
	}
	// The same online matches sent again (e.g. reconnect) do not duplicate.
	for _, m := range ms {
		_ = a.Save(m)
	}
	_, _ = a.uploadPending(ctx)
	if n, _ := st.User(u.ID).Count(ctx); n != 3 {
		t.Fatalf("server has %d matches", n)
	}
}

func TestHeartbeatSettingsAndRevocation(t *testing.T) {
	ts, st, u, tok, _ := testServer(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent.json")
	cfg := &Config{Server: ts.URL, Token: tok}
	if err := SaveConfig(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	a := New(cfg, Options{Client: &Client{Server: ts.URL, Token: tok}, ConfigPath: cfgPath,
		ConnStatus: func() (bool, string) { return true, "tcp" }})
	a.OnAutoIdentity("VirgileRL", "Epic|v|0")
	if id := a.Identity(); len(id.Names) != 1 || id.Names[0] != "VirgileRL" {
		t.Fatalf("identity not applied locally: %+v", id)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// A server-side settings write precedes the response and local persistence.
	// The heartbeat returns only after its local CAS has committed; canceling
	// Run when the server write appears can instead interrupt that response.
	a.uploadMu.Lock()
	_, err := a.heartbeat(ctx)
	a.uploadMu.Unlock()
	if err != nil {
		t.Fatalf("heartbeat did not complete: %v", err)
	}
	if raw, err := st.UserSettings(ctx, u.ID); err != nil || raw == "" {
		t.Fatalf("auto identity not stored on the server: %v", err)
	}
	saved, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("cannot read cached settings: %v", err)
	}
	if !slices.Equal(saved.Settings.PlayerNames, []string{"VirgileRL"}) ||
		!slices.Equal(saved.Settings.PlayerIDs, []string{"Epic|v|0"}) || saved.Settings.DefaultTag != "ranked" {
		t.Fatalf("settings not cached: names=%d ids=%d default_tag=%q (configuration and credentials redacted)",
			len(saved.Settings.PlayerNames), len(saved.Settings.PlayerIDs), saved.Settings.DefaultTag)
	}
	if a.report().AutoIdentity != nil {
		t.Fatal("completed heartbeat retained the pending auto identity")
	}

	devs, _ := st.User(u.ID).Devices(context.Background())
	_ = st.User(u.ID).DeleteDevice(context.Background(), devs[0].ID)
	if _, err := a.Client.Heartbeat(context.Background(), Report{}); err != ErrUnauthorized {
		t.Fatalf("revoked token: %v", err)
	}
}

func TestImportLocalDatabase(t *testing.T) {
	ts, st, u, tok, _ := testServer(t)
	local, err := store.Open(filepath.Join(t.TempDir(), "rltracker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	ctx := context.Background()
	_ = local.SaveMany(ctx, sim.Generate(sim.SeedOptions{N: 12, Seed: 5}))
	_ = local.SetManual(ctx, store.ManualDay{Day: "2026-09-01", Mode: "2v2", Games: 6, Wins: 2})
	c := &Client{Server: ts.URL, Token: tok}
	for range 2 { // importing twice does not duplicate
		n, man, err := Import(ctx, c, local, nil)
		if err != nil || n != 12 || man != 1 {
			t.Fatalf("import %d %d %v", n, man, err)
		}
	}
	if n, _ := st.User(u.ID).Count(ctx); n != 12 {
		t.Fatalf("server has %d matches", n)
	}
	if d, _ := st.User(u.ID).ListManual(ctx); len(d) != 1 || d[0].Wins != 2 {
		t.Fatalf("manual %+v", d)
	}
}

func TestNormalizeServer(t *testing.T) {
	if s, err := NormalizeServer(" https://rl.example.com/ "); err != nil || s != "https://rl.example.com" {
		t.Fatalf("%q %v", s, err)
	}
	for _, bad := range []string{"rl.example.com", "ftp://x", "https://"} {
		if _, err := NormalizeServer(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestOutboxUploadsTrainings(t *testing.T) {
	ts, st, u, tok, _ := testServer(t)
	ob, err := OpenOutbox(filepath.Join(t.TempDir(), "outbox"))
	if err != nil {
		t.Fatal(err)
	}
	a := New(&Config{Server: ts.URL, Token: tok}, Options{Client: &Client{Server: ts.URL, Token: tok}, Outbox: ob})
	t0 := time.Date(2026, 10, 1, 20, 0, 0, 0, time.UTC)
	if err := a.SaveTraining(&store.Training{StartedAt: t0, EndedAt: t0.Add(10 * time.Minute), Source: "game", Touches: 50}); err != nil {
		t.Fatal(err)
	}
	m := sim.Generate(sim.SeedOptions{N: 1, Seed: 4})[0]
	_ = a.Save(m)
	if n, err := a.uploadPending(context.Background()); err != nil || n != 2 {
		t.Fatalf("upload: %d %v", n, err)
	}
	ts2, _ := st.User(u.ID).Trainings(context.Background())
	if len(ts2) != 1 || ts2[0].Touches != 50 {
		t.Fatalf("server trainings %+v", ts2)
	}
}

func pendingIn(t *testing.T, dir string) []string {
	t.Helper()
	ob, err := OpenOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ob.Pending()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Regression: the outbox had no destination, so after switching servers the
// backlog of the previous one was sent to the new one.
func TestOutboxDestination(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "outbox")
	ob, err := OpenOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := sim.Generate(sim.SeedOptions{N: 1, Seed: 1})[0]
	// An outbox of an older version (no destination) adopts the server.
	_ = ob.Put("k1", m)
	if aside, err := ob.SetDestination("https://a.example.com", nil); err != nil || aside != "" {
		t.Fatalf("legacy outbox: %q %v", aside, err)
	}
	if p := pendingIn(t, dir); len(p) != 1 {
		t.Fatalf("legacy outbox lost its entries: %v", p)
	}
	// Same server (other spelling): kept.
	if aside, err := ob.SetDestination("HTTPS://A.example.com/", nil); err != nil || aside != "" {
		t.Fatalf("same server: %q %v", aside, err)
	}
	// Another server: the backlog is moved aside, not sent there.
	_ = ob.Put("k2", m)
	aside, err := ob.SetDestination("https://b.example.com", nil)
	if err != nil || aside == "" || filepath.Dir(aside) != filepath.Dir(dir) || !strings.Contains(filepath.Base(aside), "a.example.com") {
		t.Fatalf("switch: %q %v", aside, err)
	}
	if p := pendingIn(t, dir); len(p) != 0 {
		t.Fatalf("entries left for the new server: %v", p)
	}
	if p := pendingIn(t, aside); len(p) != 2 {
		t.Fatalf("entries moved aside: %v", p)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, destFile)); strings.TrimSpace(string(b)) != "https://b.example.com" {
		t.Fatalf("destination %q", b)
	}
}

// A running agent picking up a new agent.json: a new token for the same
// server keeps the queue; another server sets it aside.
func TestReloadConfigKeepsOrSetsAsideQueue(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent.json")
	obDir := filepath.Join(dir, "outbox")
	cfg := &Config{Server: "https://a.example.com", Token: "t1"}
	_ = SaveConfig(cfgPath, cfg)
	ob, _ := OpenOutbox(obDir)
	if _, err := ob.SetDestination(cfg.Server, nil); err != nil {
		t.Fatal(err)
	}
	a := New(cfg, Options{Client: &Client{Server: cfg.Server, Token: cfg.Token}, Outbox: ob, ConfigPath: cfgPath})
	a.reloadConfig() // notes agent.json's time
	_ = a.Save(sim.Generate(sim.SeedOptions{N: 1, Seed: 1})[0])

	rewrite := func(c Config, at time.Time) {
		t.Helper()
		if err := SaveConfig(cfgPath, &c); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(cfgPath, at, at)
		if !a.reloadConfig() {
			t.Fatal("change not picked up")
		}
	}
	rewrite(Config{Server: "https://a.example.com", Token: "t2"}, time.Now().Add(time.Hour))
	if p := pendingIn(t, obDir); len(p) != 1 {
		t.Fatalf("token change dropped the queue: %v", p)
	}
	rewrite(Config{Server: "https://b.example.com", Token: "t3"}, time.Now().Add(2*time.Hour))
	if p := pendingIn(t, obDir); len(p) != 0 {
		t.Fatalf("queue of the previous server kept for the new one: %v", p)
	}
	if s := a.Client.ServerURL(); s != "https://b.example.com" {
		t.Fatalf("target %s", s)
	}
}
