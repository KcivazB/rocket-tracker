package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func ip(v int) *int { return &v }

func TestRanks(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sc, other := s.User(LocalUser), s.User(7)
	t0 := time.Date(2026, 10, 1, 22, 0, 0, 0, time.UTC)

	for _, bad := range []Rank{
		{At: t0, Playlist: "4v4", MMR: ip(900)},                  // unknown playlist
		{At: t0, Playlist: "2v2"},                                // neither rank nor MMR
		{At: t0, Playlist: "2v2", Tier: ip(23)},                  // no such tier
		{At: t0, Playlist: "2v2", Tier: ip(22), Division: ip(1)}, // SSL has no divisions
		{At: t0, Playlist: "2v2", Division: ip(2)},               // division without tier
		{At: t0, Playlist: "2v2", Tier: ip(10), Division: ip(5)},
		{At: t0, Playlist: "2v2", MMR: ip(-3)},
	} {
		if err := sc.AddRank(ctx, &bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}

	full := &Rank{At: t0, Playlist: "2v2", Profile: " Epic|Main|0 ", Tier: ip(14), Division: ip(3), MMR: ip(1032)} // Diamond II div III
	mmrOnly := &Rank{At: t0.Add(time.Hour), Playlist: "1v1", MMR: ip(845)}
	rankOnly := &Rank{At: t0.Add(-time.Hour), Playlist: "3v3", Tier: ip(22)} // SSL
	for _, r := range []*Rank{full, mmrOnly, rankOnly} {
		if err := sc.AddRank(ctx, r); err != nil || r.ID == 0 {
			t.Fatalf("%+v: %v", r, err)
		}
	}
	if err := other.AddRank(ctx, &Rank{At: t0, Playlist: "2v2", MMR: ip(500)}); err != nil {
		t.Fatal(err)
	}

	rs, err := sc.Ranks(ctx)
	if err != nil || len(rs) != 3 {
		t.Fatalf("%+v %v", rs, err)
	}
	if rs[0].Playlist != "3v3" || *rs[0].Tier != 22 || rs[0].Division != nil || rs[0].MMR != nil {
		t.Fatalf("rank only %+v", rs[0])
	}
	if r := rs[1]; *r.Tier != 14 || *r.Division != 3 || *r.MMR != 1032 || !r.At.Equal(t0) || r.Profile != "epic|main|0" {
		t.Fatalf("full %+v", r)
	}
	if r := rs[2]; r.Tier != nil || r.Division != nil || *r.MMR != 845 {
		t.Fatalf("mmr only %+v", r)
	}

	if err := other.DeleteRank(ctx, full.ID); err != ErrNotFound {
		t.Fatalf("deleted another user's entry: %v", err)
	}
	if err := sc.DeleteRank(ctx, full.ID); err != nil {
		t.Fatal(err)
	}
	if rs, _ := sc.Ranks(ctx); len(rs) != 2 {
		t.Fatalf("after delete %d", len(rs))
	}
}
