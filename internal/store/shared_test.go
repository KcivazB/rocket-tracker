package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestShared(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var ids []int64
	for _, h := range []string{"alice", "bob", "carol"} {
		u, err := s.UpsertOIDCUser(ctx, "sub-"+h, h, h, "")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, u.ID)
	}
	alice, bob, carol := s.User(ids[0]), s.User(ids[1]), s.User(ids[2])
	t0 := time.Date(2026, 10, 1, 20, 0, 0, 0, time.UTC)
	save := func(sc *Scope, guid string, online bool, team int, pid string) *Match {
		m := &Match{GUID: guid, Online: online, StartedAt: t0, EndedAt: t0.Add(5 * time.Minute), Result: "win", MyTeam: team,
			Me: MeStats{Name: pid, PrimaryID: pid}}
		if err := sc.Save(ctx, m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	a1 := save(alice, "G1", true, 0, "Epic|a|0")
	b1 := save(bob, "G1", true, 1, "Epic|b|0") // Bob was the opponent
	save(carol, "G2", true, 0, "Epic|c|0")
	save(alice, "local-1", false, 0, "Epic|a|0")
	save(bob, "local-1", false, 0, "Epic|b|0") // offline guids are not shared matches

	sh, err := alice.Shared(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sh) != 1 || sh[0].MatchID != a1.ID || sh[0].MyTeam != 0 || len(sh[0].With) != 1 {
		t.Fatalf("shared %+v", sh)
	}
	if w := sh[0].With[0]; w.UserID != ids[1] || w.MatchID != b1.ID || w.Team != 1 || w.PrimaryID != "Epic|b|0" {
		t.Fatalf("with %+v", w)
	}
	if sh, _ := carol.Shared(ctx); len(sh) != 0 {
		t.Fatalf("carol %+v", sh)
	}

	cp, err := alice.SharedCopies(ctx, a1.ID)
	if err != nil || len(cp) != 1 || cp[0].UserID != ids[1] || cp[0].Match.ID != b1.ID || cp[0].Match.MyTeam != 1 {
		t.Fatalf("copies %+v %v", cp, err)
	}
	if _, err := alice.SharedCopies(ctx, b1.ID); err != ErrNotFound {
		t.Fatalf("another user's match id: %v", err)
	}
}
