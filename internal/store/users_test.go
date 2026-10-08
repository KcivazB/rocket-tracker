package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestScopesAreIsolated(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	alice, err := s.UpsertOIDCUser(ctx, "sub-a", "Alice", "Alice L.", "a@x")
	if err != nil {
		t.Fatal(err)
	}
	bob, _ := s.UpsertOIDCUser(ctx, "sub-b", "bob", "", "")
	a, b := s.User(alice.ID), s.User(bob.ID)
	t0 := time.Date(2026, 10, 1, 20, 0, 0, 0, time.UTC)
	// Alice and Bob played the same online match: same guid, one row each.
	ma := &Match{GUID: "G1", Online: true, StartedAt: t0, EndedAt: t0.Add(5 * time.Minute), Result: "win", Tag: "ranked", Me: MeStats{Name: "Alice"}}
	mb := &Match{GUID: "G1", Online: true, StartedAt: t0, EndedAt: t0.Add(5 * time.Minute), Result: "loss", Tag: "ranked", Me: MeStats{Name: "Bob"}}
	if err := a.Save(ctx, ma); err != nil {
		t.Fatal(err)
	}
	if err := b.Save(ctx, mb); err != nil {
		t.Fatal(err)
	}
	if ma.ID == mb.ID {
		t.Fatal("guid upsert crossed users")
	}
	if _, err := b.Get(ctx, ma.ID); err != ErrNotFound {
		t.Fatalf("bob read alice's match: %v", err)
	}
	if _, err := b.SetTag(ctx, ma.ID, "casual"); err != ErrNotFound {
		t.Fatalf("bob tagged alice's match: %v", err)
	}
	if err := b.Delete(ctx, ma.ID); err != ErrNotFound {
		t.Fatalf("bob deleted alice's match: %v", err)
	}
	if n, _ := a.Count(ctx); n != 1 {
		t.Fatalf("alice count %d", n)
	}
	if n, _ := s.Count(ctx); n != 0 {
		t.Fatalf("local user sees %d server matches", n)
	}
	if err := b.SetManual(ctx, ManualDay{Day: "2026-10-01", Mode: "1v1", Games: 3, Wins: 1}); err != nil {
		t.Fatal(err)
	}
	if err := a.SetManual(ctx, ManualDay{Day: "2026-10-01", Mode: "1v1", Games: 5, Wins: 5}); err != nil {
		t.Fatal(err)
	}
	if d, _ := b.ListManual(ctx); len(d) != 1 || d[0].Games != 3 {
		t.Fatalf("bob manual %+v", d)
	}
}

func TestIngestDedupesBySourceKey(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	u, _ := s.UpsertOIDCUser(ctx, "sub", "u", "", "")
	sc := s.User(u.ID)
	t0 := time.Date(2026, 10, 1, 20, 0, 0, 0, time.UTC)
	m := &Match{GUID: "local-1", StartedAt: t0, EndedAt: t0.Add(5 * time.Minute), Result: "win", Tag: "casual"}
	if err := sc.Ingest(ctx, m, "pc1:42"); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.SetTag(ctx, m.ID, "private"); err != nil {
		t.Fatal(err)
	}
	again := &Match{GUID: "local-1", StartedAt: t0, EndedAt: t0.Add(5 * time.Minute), Result: "win", Tag: "casual", TeamScore: 2}
	if err := sc.Ingest(ctx, again, "pc1:42"); err != nil {
		t.Fatal(err)
	}
	ms, _ := sc.List(ctx)
	if len(ms) != 1 || ms[0].Tag != "private" || ms[0].TeamScore != 2 {
		t.Fatalf("%d matches, %+v", len(ms), ms)
	}
}

func TestUsersSessionsDevices(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	u1, _ := s.UpsertOIDCUser(ctx, "s1", "Jean Dupont", "", "")
	u2, _ := s.UpsertOIDCUser(ctx, "s2", "jean.dupont@example.com", "", "")
	u3, _ := s.UpsertOIDCUser(ctx, "s3", "jean-dupont", "", "")
	if u1.Handle != "jean-dupont" || u2.Handle != "jean-dupont-2" || u3.Handle != "jean-dupont-3" {
		t.Fatalf("handles %q %q %q", u1.Handle, u2.Handle, u3.Handle)
	}
	again, _ := s.UpsertOIDCUser(ctx, "s1", "renamed", "New Name", "")
	if again.ID != u1.ID || again.Handle != "jean-dupont" || again.Name != "New Name" {
		t.Fatalf("relogin %+v", again)
	}
	for in, want := range map[string]string{"Chloé": "chloe", "Zoë Müller": "zoe-muller", "@@@": "player", "a.b_c": "a-b_c"} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
	if got, err := s.UserByHandle(ctx, "JEAN-DUPONT-2"); err != nil || got.ID != u2.ID {
		t.Fatalf("by handle %v %v", got, err)
	}

	tok, err := s.CreateSession(ctx, u1.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.SessionUser(ctx, tok, time.Hour); err != nil || got.ID != u1.ID {
		t.Fatalf("session %v %v", got, err)
	}
	if _, err := s.SessionUser(ctx, tok+"x", time.Hour); err != ErrNotFound {
		t.Fatal("bad token accepted")
	}
	old, _ := s.CreateSession(ctx, u1.ID, -time.Minute)
	if _, err := s.SessionUser(ctx, old, time.Hour); err != ErrNotFound {
		t.Fatal("expired session accepted")
	}
	_ = s.DeleteSession(ctx, tok)
	if _, err := s.SessionUser(ctx, tok, time.Hour); err != ErrNotFound {
		t.Fatal("deleted session accepted")
	}

	d, dtok, err := s.User(u2.ID).CreateDevice(ctx, "  Gaming PC ")
	if err != nil || d.Name != "Gaming PC" {
		t.Fatalf("device %+v %v", d, err)
	}
	if got, dev, err := s.DeviceUser(ctx, dtok); err != nil || got.ID != u2.ID || dev.ID != d.ID || dev.LastSeenAt == "" {
		t.Fatalf("device user %v %v %v", got, dev, err)
	}
	if err := s.User(u1.ID).DeleteDevice(ctx, d.ID); err != ErrNotFound {
		t.Fatal("other user revoked the device")
	}
	if err := s.User(u2.ID).DeleteDevice(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.DeviceUser(ctx, dtok); err != ErrNotFound {
		t.Fatal("revoked token accepted")
	}
	if err := s.SetUserSettings(ctx, u1.ID, `{"a":1}`); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.UserSettings(ctx, u1.ID); v != `{"a":1}` {
		t.Fatalf("settings %q", v)
	}
}

func TestLeaderboardAndSummaries(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	a, _ := s.UpsertOIDCUser(ctx, "a", "a", "", "")
	b, _ := s.UpsertOIDCUser(ctx, "b", "b", "", "")
	now := time.Now().UTC()
	mk := func(mode, result string, ago time.Duration, online bool, goals int) *Match {
		t0 := now.Add(-ago)
		return &Match{Online: online, StartedAt: t0, EndedAt: t0.Add(5 * time.Minute), Mode: mode, Variant: "Soccar", Result: result,
			Tag: "ranked", TeamScore: 3, OppScore: 1, Me: MeStats{Name: "A", Goals: goals}}
	}
	_ = s.User(a.ID).SaveMany(ctx, []*Match{
		mk("1v1", "win", time.Hour, true, 2), mk("1v1", "loss", 2*time.Hour, true, 0), mk("2v2", "win", time.Hour, true, 1),
		mk("1v1", "abandoned", time.Hour, true, 9), mk("1v1", "win", time.Hour, false, 5), mk("1v1", "win", 40*24*time.Hour, true, 1),
	})
	_ = s.User(b.ID).SaveMany(ctx, []*Match{mk("1v1", "loss", time.Hour, true, 0)})
	_ = s.SaveMany(ctx, []*Match{mk("1v1", "win", time.Hour, true, 0)}) // local user: never ranked

	rows, err := s.Leaderboard(ctx, LeaderboardQuery{Mode: "1v1", Since: now.Add(-30 * 24 * time.Hour)})
	if err != nil || len(rows) != 2 {
		t.Fatalf("%v %+v", err, rows)
	}
	var ra LeaderRow
	for _, r := range rows {
		if r.UserID == a.ID {
			ra = r
		}
	}
	if ra.Wins != 1 || ra.Losses != 1 || ra.Abandoned != 1 || ra.GoalsAvg != 1 || ra.GoalDiffAvg != 2 {
		t.Fatalf("alice row %+v", ra)
	}
	if rows, _ := s.Leaderboard(ctx, LeaderboardQuery{Mode: "other"}); len(rows) != 0 {
		t.Fatalf("other %+v", rows)
	}
	if rows, _ := s.Leaderboard(ctx, LeaderboardQuery{Tag: "casual"}); len(rows) != 0 {
		t.Fatalf("casual %+v", rows)
	}
	sum, err := s.PlayerSummaries(ctx)
	if err != nil || sum[a.ID].Matches != 6 || sum[b.ID].Matches != 1 || len(sum[a.ID].GameNames) != 1 || sum[0] != nil {
		t.Fatalf("%v %+v", err, sum)
	}
}

// A database written by v0.3 (schema v2) keeps its data, owned by the local user.
func TestMigrateFromV2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range migrations[:2] {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.Exec(`PRAGMA user_version = 2;
		INSERT INTO matches (guid, online, started_at, ended_at, mode, result, tag, data) VALUES
		('G', 1, '2026-10-01T20:00:00Z', '2026-10-01T20:05:00Z', '1v1', 'win', 'ranked',
		 '{"guid":"G","online":true,"mode":"1v1","variant":"Soccar","result":"win","team_score":3,"opp_score":1,"mvp":true,"me":{"name":"V","goals":2,"score":420}}');
		INSERT INTO manual_days (day, mode, games, wins) VALUES ('2026-10-02', '1v1', 4, 2);
		INSERT INTO kv (k, v) VALUES ('repair:goals-speeds-v1', 'done');`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if ms, _ := s.List(ctx); len(ms) != 1 || ms[0].Me.Goals != 2 {
		t.Fatalf("matches %+v", ms)
	}
	if d, _ := s.ListManual(ctx); len(d) != 1 || d[0].Games != 4 {
		t.Fatalf("manual %+v", d)
	}
	var goals, score, mvp int
	var variant string
	if err := s.db.QueryRow(`SELECT me_goals, me_score, mvp, variant FROM matches`).Scan(&goals, &score, &mvp, &variant); err != nil ||
		goals != 2 || score != 420 || mvp != 1 || variant != "Soccar" {
		t.Fatalf("backfill %d %d %d %q %v", goals, score, mvp, variant, err)
	}
}
