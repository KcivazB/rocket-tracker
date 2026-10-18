package agent

import (
	"context"
	"path/filepath"
	"rocket-tracker/internal/domain"
	"rocket-tracker/internal/sim"
	"rocket-tracker/internal/store"
	"testing"
)

func TestJournalOutageRotationAndAccountIsolation(t *testing.T) {
	ts, remote, u, tok, down := testServer(t)
	ctx := context.Background()
	local, e := store.Open(filepath.Join(t.TempDir(), "local.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer local.Close()
	scope := local.User(0)
	sid, e := remote.InstallationID(ctx)
	if e != nil {
		t.Fatal(e)
	}
	d := domain.Destination{Server: ts.URL, ServerID: sid, AccountID: u.ID}
	cfg := &Config{Server: ts.URL, Token: tok, ServerID: sid, AccountID: u.ID}
	a := New(cfg, Options{Client: &Client{Server: ts.URL, Token: tok}, Journal: scope})
	m := sim.Generate(sim.SeedOptions{N: 1, Seed: 45})[0]
	if e := scope.RecordResult(ctx, m, &d); e != nil {
		t.Fatal(e)
	}
	down.Store(true)
	if n, e := a.uploadPending(ctx); e == nil || n != 0 {
		t.Fatal("outage was acknowledged")
	}
	down.Store(false)
	other, e := remote.UpsertOIDCUser(ctx, "other-sub", "other", "Other", "")
	if e != nil {
		t.Fatal(e)
	}
	_, otherTok, e := remote.User(other.ID).CreateDevice(ctx, "other PC")
	if e != nil {
		t.Fatal(e)
	}
	a.Client.SetTarget(ts.URL, otherTok)
	if n, e := a.uploadPending(ctx); e != nil || n != 0 {
		t.Fatal("sent to another account", n, e)
	}
	if n, _ := remote.User(other.ID).Count(ctx); n != 0 {
		t.Fatal("foreign account received payload")
	}
	pending, _ := scope.PendingDeliveries(ctx, d)
	if len(pending) != 1 {
		t.Fatal("foreign account removed intent")
	}
	_, rotated, e := remote.User(u.ID).CreateDevice(ctx, "replacement token")
	if e != nil {
		t.Fatal(e)
	}
	a.Client.SetTarget(ts.URL, rotated)
	if n, e := a.uploadPending(ctx); e != nil || n != 1 {
		t.Fatal("same-account rotation lost delivery", n, e)
	}
	if n, _ := remote.User(u.ID).Count(ctx); n != 1 {
		t.Fatal("missing remote result")
	}
	if p, _ := scope.PendingDeliveries(ctx, d); len(p) != 0 {
		t.Fatal("ack did not complete exact revision")
	}
}
