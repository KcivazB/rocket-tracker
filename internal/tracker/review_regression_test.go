package tracker

import (
	"context"
	"errors"
	"path/filepath"
	"rocket-tracker/internal/domain"
	"rocket-tracker/internal/store"
	"testing"
	"time"
)

func TestIntervalCapSplitReplayIsIdempotent(t *testing.T) {
	for _, local := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-sender-key", true: "local-no-key"}[local], func(t *testing.T) {
			ctx := context.Background()
			db, err := store.Open(filepath.Join(t.TempDir(), "review.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			sc := db.User(0)
			at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			a := domain.Training{StartedAt: at, EndedAt: at.Add(8191 * time.Second), Source: "game", Profile: "a", ActiveIntervals: make([]domain.TrainingInterval, domain.MaxTrainingIntervals)}
			for i := range a.ActiveIntervals {
				a.ActiveIntervals[i] = domain.TrainingInterval{StartedAt: at.Add(time.Duration(2*i) * time.Second), EndedAt: at.Add(time.Duration(2*i+1) * time.Second)}
			}
			b := domain.Training{StartedAt: at.Add(8192 * time.Second), EndedAt: at.Add(8193 * time.Second), Source: "game", Profile: "a", ActiveIntervals: []domain.TrainingInterval{{StartedAt: at.Add(8192 * time.Second), EndedAt: at.Add(8193 * time.Second)}}}
			save := func(v *domain.Training) error {
				if local {
					return sc.RecordTraining(ctx, v, nil)
				}
				return sc.AddTraining(ctx, v, "stable-sender-key")
			}
			if err := save(&a); err != nil {
				t.Fatal(err)
			}
			originalB := b
			if err := save(&b); err != nil {
				t.Fatal(err)
			}
			rows, err := sc.Trainings(ctx)
			if err != nil || len(rows) != 2 {
				t.Fatalf("split rows=%d err=%v", len(rows), err)
			}
			replay := originalB
			if err := save(&replay); err != nil {
				t.Fatal(err)
			}
			rows, err = sc.Trainings(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var active time.Duration
			for _, v := range rows {
				active += v.ActiveDuration()
			}
			t.Logf("first=%d split=%d replay=%d rows=%d active=%s", a.ID, b.ID, replay.ID, len(rows), active)
			if len(rows) != 2 || replay.ID != b.ID {
				t.Errorf("identical replay must retain 2 rows and row %d; got %d rows, replay %d", b.ID, len(rows), replay.ID)
			}
		})
	}
}
func TestFailedTrainingDoesNotBlockLaterMatch(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "review.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sc := db.User(0)
	h := newHarness(t)
	h.id = Identity{Names: []string{"me"}}
	h.tr.o.SaveTraining = func(v *domain.Training) error { return sc.RecordTraining(ctx, v, nil) }
	h.tr.o.Save = func(v *domain.Match) error { return sc.RecordResult(ctx, v, nil) }
	solo := G{Players: []P{me}}
	h.update(solo)
	h.advance(13 * time.Hour)
	h.update(solo)
	h.send("MatchDestroyed", map[string]any{})
	if !errors.Is(h.tr.SaveError(), domain.ErrInvalidTraining) {
		t.Fatalf("expected invalid training, got %v", h.tr.SaveError())
	}
	g := G{Guid: "valid-next", Players: []P{me, mt, op1, op2}}
	h.send("MatchCreated", map[string]any{"MatchGuid": g.Guid})
	h.playClock(g, 300, 250)
	h.send("MatchEnded", map[string]any{"MatchGuid": g.Guid, "WinnerTeamNum": 0})
	flushErr := h.tr.Flush()
	n, err := sc.Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("valid matches committed=%d pending=%d flush=%v", n, h.tr.PendingRecords(), flushErr)
	if !errors.Is(flushErr, domain.ErrInvalidTraining) || h.tr.PendingRecords() != 1 {
		t.Fatalf("unresolved invalid training must stay observable: pending=%d err=%v", h.tr.PendingRecords(), flushErr)
	}
	if n != 1 {
		t.Errorf("valid next match must commit independently; got %d", n)
	}
}
