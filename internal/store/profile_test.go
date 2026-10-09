package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestLeaderboardProfiles(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	u, err := s.UpsertOIDCUser(ctx, "sub-a", "alice", "Alice", "")
	if err != nil {
		t.Fatal(err)
	}
	sc := s.User(u.ID)
	t0 := time.Date(2026, 10, 1, 20, 0, 0, 0, time.UTC)
	// Main account: 3 wins scoring 300; alt account: 1 loss scoring 100.
	for i, x := range []struct {
		pid, res string
		score    int
	}{{"Epic|Main|0", "win", 300}, {"Epic|main|0", "win", 300}, {"Epic|Main|0", "win", 300}, {"Steam|alt|0", "loss", 100}} {
		m := &Match{GUID: fmt.Sprintf("G%d", i), Online: true, StartedAt: t0.Add(time.Duration(i) * time.Hour), Result: x.res,
			Mode: "2v2", Me: MeStats{PrimaryID: x.pid, Score: x.score}}
		if err := sc.Save(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if k := ProfileKey(&Match{Me: MeStats{PrimaryID: " Epic|MAIN|0 "}}); k != "epic|main|0" {
		t.Fatalf("key %q", k)
	}
	if k := ProfileKey(&Match{Me: MeStats{Name: "Bot"}}); k != "name:bot" {
		t.Fatalf("name key %q", k)
	}

	rows, err := s.Leaderboard(ctx, LeaderboardQuery{})
	if err != nil || len(rows) != 1 || rows[0].Wins != 3 || rows[0].Losses != 1 || rows[0].ScoreAvg != 250 {
		t.Fatalf("all accounts merged: %+v %v", rows, err)
	}
	rows, _ = s.Leaderboard(ctx, LeaderboardQuery{Profiles: map[int64]map[string]bool{u.ID: {"epic|main|0": true}}})
	if len(rows) != 1 || rows[0].Wins != 3 || rows[0].Losses != 0 || rows[0].ScoreAvg != 300 {
		t.Fatalf("main account only: %+v", rows)
	}
	rows, _ = s.Leaderboard(ctx, LeaderboardQuery{Profiles: map[int64]map[string]bool{u.ID: {"epic|other|0": true}}})
	if len(rows) != 0 {
		t.Fatalf("unknown account: %+v", rows)
	}
}
