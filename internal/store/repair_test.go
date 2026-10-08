package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// A match as stored by v0.1/v0.2 from the real game (shape of match #7 of
// the first real database): every goal sent twice, full then empty, the
// teams shifted by the duplicates, speeds in km/h. Real score 1-3, stored 1-7.
func brokenMatch() *Match {
	return &Match{
		GUID: "G1", Online: true, Mode: "1v1", Result: "loss", MyTeam: 0,
		StartedAt: time.Date(2026, 10, 8, 17, 46, 0, 0, time.UTC), EndedAt: time.Date(2026, 10, 8, 17, 54, 0, 0, time.UTC),
		TeamScore: 1, OppScore: 7,
		Me: MeStats{Name: "zaviik.", Score: 250, Goals: 1},
		Players: []Player{
			{Name: "zaviik.", Team: 0, Score: 250, Goals: 1, IsMe: true},
			{Name: "Opp", Team: 1, Score: 900, Goals: 6},
		},
		Movement: &Movement{AvgSpeed: 54, GroundPct: 60},
		Hits:     &Hits{Count: 20, AvgSpeed: 70, MaxSpeed: 110},
		Goals: []Goal{
			{T: 11, Team: "them", Scorer: "Opp", Speed: 80},
			{T: 11, Team: "them"},
			{T: 49, Team: "us", Scorer: "Opp", Speed: 83}, // team shifted
			{T: 49, Team: "them"},
			{T: 145, Team: "them", Scorer: "zaviik.", Speed: 72}, // my goal, shifted
			{T: 145, Team: "them"},
			{T: 233, Team: "them", Scorer: "Opp", Speed: 100},
			{T: 234, Team: "them"}, // duplicate one second later
		},
	}
}

func TestRepairGoalsAndSpeeds(t *testing.T) {
	m := brokenMatch()
	if !repairGoalsAndSpeeds(m) {
		t.Fatal("expected a change")
	}
	m.Normalize()
	if m.TeamScore != 1 || m.OppScore != 3 || m.GoalDiff != -2 || len(m.Goals) != 4 || m.FirstGoal != "them" {
		t.Fatalf("score %d-%d diff %d goals %d first %s", m.TeamScore, m.OppScore, m.GoalDiff, len(m.Goals), m.FirstGoal)
	}
	for i, want := range []string{"them", "them", "us", "them"} {
		if m.Goals[i].Team != want {
			t.Fatalf("goal %d team %s, want %s (%+v)", i, m.Goals[i].Team, want, m.Goals)
		}
	}
	if !m.Goals[2].MeScored || m.Goals[0].MeScored {
		t.Fatalf("me_scored %+v", m.Goals)
	}
	if p := m.Players[1]; p.Goals != 3 || p.Score != 600 {
		t.Fatalf("opponent %+v", p)
	}
	if m.Me.Goals != 1 || m.Me.Score != 250 {
		t.Fatalf("me %+v", m.Me)
	}
	if m.Movement.AvgSpeed != 1500 || m.Hits.AvgSpeed != 1944.4 || m.Hits.MaxSpeed != 3055.6 || m.Goals[0].Speed != 2222.2 {
		t.Fatalf("speeds %v %v %v %v", m.Movement.AvgSpeed, m.Hits.AvgSpeed, m.Hits.MaxSpeed, m.Goals[0].Speed)
	}
	// Idempotent: a repaired (or correct) match is left alone.
	if repairGoalsAndSpeeds(m) {
		t.Fatalf("second pass changed %+v", m)
	}
}

func TestRepairRunsOnceAtOpen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db.sqlite")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Save(ctx, brokenMatch()); err != nil {
		t.Fatal(err)
	}
	// Pretend the database comes from a version without the repair.
	if _, err := st.db.Exec(`DELETE FROM kv WHERE k = 'repair:goals-speeds-v1'`); err != nil {
		t.Fatal(err)
	}
	st.Close()

	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ms, err := st.List(ctx)
	if err != nil || len(ms) != 1 {
		t.Fatalf("list %v %d", err, len(ms))
	}
	if m := ms[0]; m.TeamScore != 1 || m.OppScore != 3 || m.Movement.AvgSpeed != 1500 {
		t.Fatalf("not repaired: %d-%d speed %v", m.TeamScore, m.OppScore, m.Movement.AvgSpeed)
	}
	// Already-correct uu/s data saved afterwards must not be touched on reopen.
	ok := brokenMatch()
	ok.GUID, ok.Goals, ok.TeamScore, ok.OppScore = "G2", nil, 2, 1
	ok.Movement.AvgSpeed, ok.Hits.AvgSpeed, ok.Hits.MaxSpeed = 60, 70, 110 // would look like km/h
	if err := st.Save(ctx, ok); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m, err := st.Get(ctx, ok.ID)
	if err != nil || m.Movement.AvgSpeed != 60 {
		t.Fatalf("repair ran twice: %v %+v", err, m.Movement)
	}
}
