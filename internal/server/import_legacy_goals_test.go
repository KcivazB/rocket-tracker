package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"rocket-tracker/internal/store"
)

func TestImportLegacyGoalsReadOnlyReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE matches(id INTEGER PRIMARY KEY,tag TEXT,data TEXT); PRAGMA user_version=1`); err != nil {
		t.Fatal(err)
	}
	m := ingestionFixture()
	m.MyTeam = 0
	m.TeamScore = 1
	m.OppScore = 7
	m.Me = store.MeStats{Name: "Synthetic Me", Score: 250, Goals: 1}
	m.Players = []store.Player{{Name: "Synthetic Me", Team: 0, Score: 250, Goals: 1, IsMe: true}, {Name: "Synthetic Opp", Team: 1, Score: 900, Goals: 6}}
	m.Movement = &store.Movement{AvgSpeed: 54}
	m.Hits = &store.Hits{Count: 20, AvgSpeed: 70, MaxSpeed: 110}
	m.Goals = []store.Goal{{T: 11, Team: "them", Scorer: "Synthetic Opp", Speed: 80}, {T: 11, Team: "them"}, {T: 49, Team: "us", Scorer: "Synthetic Opp", Speed: 83}, {T: 49, Team: "them"}, {T: 145, Team: "them", Scorer: "Synthetic Me", Speed: 72}, {T: 145, Team: "them"}, {T: 233, Team: "them", Scorer: "Synthetic Opp", Speed: 100}, {T: 234, Team: "them"}}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO matches VALUES(1,'ranked',?)`, string(raw)); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, sc, _ := ingestionHandler(t)
	im := Importer{Scope: sc}
	var previous *store.Match
	for range 2 {
		result, err := im.Database(context.Background(), path)
		if err != nil || result.Matches != 1 {
			t.Fatalf("import=%+v err=%v", result, err)
		}
		rows, err := sc.List(context.Background())
		if err != nil || len(rows) != 1 {
			t.Fatalf("rows=%d err=%v", len(rows), err)
		}
		got := rows[0]
		if got.TeamScore != 1 || got.OppScore != 3 || got.GoalDiff != -2 || len(got.Goals) != 4 || !got.Goals[2].MeScored || got.Players[1].Goals != 3 || got.Players[1].Score != 600 {
			t.Fatalf("legacy repair lost: %+v", got)
		}
		if got.Movement.AvgSpeed != 54 || got.Hits.MaxSpeed != 110 || got.Goals[0].Speed != 80 {
			t.Fatal("unknown speed was converted heuristically")
		}
		if previous != nil && !reflect.DeepEqual(previous, got) {
			t.Fatal("replay changed repaired data")
		}
		previous = got
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("legacy source mutated")
	}
	correct := *previous
	if store.RepairImportedGoals(&correct) || !reflect.DeepEqual(previous, &correct) {
		t.Fatal("modern full goals changed")
	}
}
