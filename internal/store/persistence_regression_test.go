package store

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"rocket-tracker/internal/application"
	"rocket-tracker/internal/domain"
)

func TestRecordingOriginsAllDirectionsAndReopen(t *testing.T) {
	for _, ack := range []bool{false, true} {
		for _, firstLocal := range []bool{false, true} {
			t.Run(fmt.Sprintf("ack=%v/local-first=%v", ack, firstLocal), func(t *testing.T) {
				ctx, s, sc, d, m := deliveryFixture(t)
				var first, second *domain.Destination = &d, nil
				if firstLocal {
					first, second = second, first
				}
				a := *m
				ta := Training{StartedAt: m.StartedAt, EndedAt: m.EndedAt, Source: "game", Profile: "same", Touches: 10}
				if err := sc.RecordResult(ctx, &a, first); err != nil {
					t.Fatal(err)
				}
				if err := sc.RecordTraining(ctx, &ta, first); err != nil {
					t.Fatal(err)
				}
				if ack && first != nil {
					pending, err := sc.PendingDeliveries(ctx, d)
					if err != nil {
						t.Fatal(err)
					}
					for _, v := range pending {
						if err := sc.CompleteDelivery(ctx, v, false); err != nil {
							t.Fatal(err)
						}
					}
				}
				var path string
				if err := s.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path); err != nil {
					t.Fatal(err)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				restored, err := Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer restored.Close()
				sc = restored.User(0)
				b := *m
				b.TeamScore = 99
				tb := Training{StartedAt: m.StartedAt.Add(2 * time.Minute), EndedAt: m.StartedAt.Add(3 * time.Minute), Source: "game", Profile: "same", Touches: 99}
				if err := sc.RecordResult(ctx, &b, second); err != nil {
					t.Fatal(err)
				}
				if err := sc.RecordTraining(ctx, &tb, second); err != nil {
					t.Fatal(err)
				}
				if a.ID == b.ID || ta.ID == tb.ID {
					t.Fatal("cross-origin merge", a.ID, b.ID, ta.ID, tb.ID)
				}
				// Returning to the first origin must not absorb the other record.
				c := *m
				c.TeamScore = 1
				tc := Training{StartedAt: m.StartedAt.Add(3 * time.Minute), EndedAt: m.StartedAt.Add(4 * time.Minute), Source: "game", Profile: "same", Touches: 1}
				if err := sc.RecordResult(ctx, &c, first); err != nil {
					t.Fatal(err)
				}
				if err := sc.RecordTraining(ctx, &tc, first); err != nil {
					t.Fatal(err)
				}
				if c.ID != a.ID || tc.ID != ta.ID || tc.Touches != 10 {
					t.Fatalf("origin not stable: %+v", tc)
				}
				if n, _ := sc.Count(ctx); n != 2 {
					t.Fatal(n)
				}
			})
		}
	}
}

func TestHistoricalOriginIsNotLocalOnly(t *testing.T) {
	ctx, _, sc, d, m := deliveryFixture(t)
	if err := sc.Save(ctx, m); err != nil {
		t.Fatal(err)
	}
	oldID := m.ID
	tr := Training{StartedAt: m.StartedAt, EndedAt: m.EndedAt, Source: "game"}
	if err := sc.AddTraining(ctx, &tr, ""); err != nil {
		t.Fatal(err)
	}
	oldTrainingID := tr.ID
	for _, dest := range []*domain.Destination{nil, &d} {
		next := *m
		nextTraining := Training{StartedAt: tr.StartedAt, EndedAt: tr.EndedAt, Source: "game"}
		if err := sc.RecordResult(ctx, &next, dest); err != nil {
			t.Fatal(err)
		}
		if err := sc.RecordTraining(ctx, &nextTraining, dest); err != nil {
			t.Fatal(err)
		}
		if next.ID == oldID || nextTraining.ID == oldTrainingID {
			t.Fatal("historical origin was adopted")
		}
	}
	// Server/import nil remains compatible with historical rows.
	retry := *m
	if err := sc.Ingest(ctx, &retry, "import"); err != nil {
		t.Fatal(err)
	}
	if retry.ID != oldID {
		t.Fatal("server nil changed historical identity")
	}
	again := Training{StartedAt: tr.StartedAt, EndedAt: tr.EndedAt, Source: "game"}
	if err := sc.AddTraining(ctx, &again, "import"); err != nil {
		t.Fatal(err)
	}
	if again.ID != oldTrainingID {
		t.Fatal("server nil changed historical training identity")
	}
}

func TestTransportKeyCannotOverrideExplicitMatchProfile(t *testing.T) {
	for _, online := range []bool{false, true} {
		ctx, _, sc, _, a := deliveryFixture(t)
		a.Online = online
		a.Me.PrimaryID = "a"
		b := *a
		b.Me.PrimaryID = "b"
		for _, v := range []*Match{a, &b, a, &b} {
			if err := application.IngestResult(ctx, sc, "ranked", "same-transport", v); err != nil {
				t.Fatal(err)
			}
		}
		rows, err := sc.List(ctx)
		if err != nil || len(rows) != 2 {
			t.Fatalf("online=%v rows=%v err=%v", online, rows, err)
		}
		if rows[0].Me.PrimaryID == rows[1].Me.PrimaryID {
			t.Fatal("profile replaced")
		}
	}
}

func TestTrainingSplitReplayAndFocus(t *testing.T) {
	ctx, _, sc, d, m := deliveryFixture(t)
	a := Training{StartedAt: m.StartedAt, EndedAt: m.StartedAt.Add(11*time.Hour + 59*time.Minute), Source: "game", Profile: "same"}
	b := Training{StartedAt: m.StartedAt.Add(12 * time.Hour), EndedAt: m.StartedAt.Add(12*time.Hour + 5*time.Minute), Source: "game", Profile: "same"}
	originalA, originalB := a, b
	for _, v := range []*Training{&a, &b} {
		if err := sc.RecordTraining(ctx, v, &d); err != nil {
			t.Fatal(err)
		}
	}
	if a.ID == b.ID {
		t.Fatal("oversized merge")
	}
	if _, err := sc.SetTrainingFocus(ctx, b.ID, "shooting"); err != nil {
		t.Fatal(err)
	}
	for _, v := range []*Training{&originalA, &originalB} {
		if err := sc.RecordTraining(ctx, v, &d); err != nil {
			t.Fatal(err)
		}
	}
	if originalB.ID != b.ID || originalB.Focus != "shooting" {
		t.Fatal("split replay lost focus/identity")
	}
	pending, err := sc.PendingDeliveries(ctx, d)
	if err != nil || len(pending) != 2 {
		t.Fatal(pending, err)
	}
	target, err := Open(filepath.Join(t.TempDir(), "remote.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	for _, v := range pending {
		if err := application.IngestTraining(ctx, target.User(1), v.Key, v.Training); err != nil {
			t.Fatalf("undeliverable split: %v", err)
		}
	}
}

func TestTrainingMergedKeysSurviveSplit(t *testing.T) {
	ctx, _, sc, _, m := deliveryFixture(t)
	at := m.StartedAt
	records := []Training{
		{StartedAt: at, EndedAt: at.Add(6 * time.Hour), Source: "timer", Profile: "a"},
		{StartedAt: at.Add(6*time.Hour + time.Minute), EndedAt: at.Add(11*time.Hour + 59*time.Minute), Source: "game", Profile: "a"},
		{StartedAt: at.Add(12 * time.Hour), EndedAt: at.Add(12*time.Hour + 5*time.Minute), Source: "game", Profile: "a"},
	}
	for i := range records {
		v := records[i]
		if err := sc.AddTraining(ctx, &v, fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := sc.Trainings(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	if _, err := sc.SetTrainingFocus(ctx, rows[0].ID, "saves"); err != nil {
		t.Fatal(err)
	}
	// The second sender key was merged into row one; replay must find it there.
	for i := range records {
		v := records[i]
		if err := sc.AddTraining(ctx, &v, fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	rows, err = sc.Trainings(ctx)
	if err != nil || len(rows) != 2 || rows[0].Focus != "saves" {
		t.Fatal(rows, err)
	}
}

func TestActiveIntervalsPersistAndQueue(t *testing.T) {
	ctx, _, sc, d, m := deliveryFixture(t)
	tr := Training{StartedAt: m.StartedAt, EndedAt: m.StartedAt.Add(10 * time.Minute), Source: "timer", ActiveIntervals: []domain.TrainingInterval{}}
	if err := sc.RecordTraining(ctx, &tr, &d); err != nil {
		t.Fatal(err)
	}
	rows, err := sc.Trainings(ctx)
	if err != nil || len(rows) != 1 || rows[0].ActiveIntervals == nil || rows[0].ActiveDuration() != 0 {
		t.Fatal(rows, err)
	}
	pending, err := sc.PendingDeliveries(ctx, d)
	if err != nil || len(pending) != 1 || pending[0].Training.ActiveIntervals == nil {
		t.Fatal(pending, err)
	}
}

func TestLocalOnlyOriginCommitsAtomically(t *testing.T) {
	ctx, s, sc, _, m := deliveryFixture(t)
	if _, err := s.db.Exec(`CREATE TRIGGER fail_origin BEFORE INSERT ON record_origins BEGIN SELECT RAISE(ABORT,'injected origin failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := sc.RecordResult(ctx, m, nil); err == nil {
		t.Fatal("local result committed without explicit origin")
	}
	tr := Training{StartedAt: m.StartedAt, EndedAt: m.EndedAt, Source: "timer"}
	if err := sc.RecordTraining(ctx, &tr, nil); err == nil {
		t.Fatal("local training committed without explicit origin")
	}
	if n, _ := sc.Count(ctx); n != 0 {
		t.Fatal("partial result", n)
	}
	if n, _ := sc.TrainingCount(ctx); n != 0 {
		t.Fatal("partial training", n)
	}
}

func TestUpgradeV9RetainsOriginAndBackfillsTrainingKeys(t *testing.T) {
	ctx, s, sc, d, m := deliveryFixture(t)
	old := Training{StartedAt: m.StartedAt, EndedAt: m.EndedAt, Source: "timer", Focus: "shooting", Profile: "a"}
	if err := sc.AddTraining(ctx, &old, "legacy-key"); err != nil {
		t.Fatal(err)
	}
	if err := sc.RecordResult(ctx, m, &d); err != nil {
		t.Fatal(err)
	}
	pending, err := sc.PendingDeliveries(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range pending {
		if err := sc.CompleteDelivery(ctx, v, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`DROP TABLE training_sources; PRAGMA user_version=9`); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM training_sources WHERE src_key='legacy-key' AND record_id=?`, old.ID).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	replay := Training{StartedAt: old.StartedAt, EndedAt: old.EndedAt, Source: "timer", Profile: "a"}
	if err := sc.AddTraining(ctx, &replay, "legacy-key"); err != nil || replay.ID != old.ID || replay.Focus != "shooting" {
		t.Fatal(replay, err)
	}
	originalID := m.ID
	if err := sc.RecordResult(ctx, m, nil); err != nil || m.ID == originalID {
		t.Fatal("acked origin became local during upgrade", err)
	}
}
