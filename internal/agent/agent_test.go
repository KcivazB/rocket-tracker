package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	ob, _ := OpenOutbox(filepath.Join(dir, "outbox"))
	a := New(cfg, Options{Client: &Client{Server: ts.URL, Token: tok}, Outbox: ob, ConfigPath: cfgPath,
		ConnStatus: func() (bool, string) { return true, "tcp" }})
	a.OnAutoIdentity("VirgileRL", "Epic|v|0")
	if id := a.Identity(); len(id.Names) != 1 || id.Names[0] != "VirgileRL" {
		t.Fatalf("identity not applied locally: %+v", id)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, _ := st.UserSettings(context.Background(), u.ID)
		if raw != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("auto identity never reached the server")
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
	saved, err := LoadConfig(cfgPath)
	if err != nil || len(saved.Settings.PlayerIDs) != 1 || saved.Settings.DefaultTag != "ranked" {
		t.Fatalf("settings not cached: %+v %v", saved, err)
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
