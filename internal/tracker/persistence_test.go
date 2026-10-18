package tracker

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"rocket-tracker/internal/domain"
)

func TestFailedResultRetainedAcrossNewEventsAndFlush(t *testing.T) {
	h := newHarness(t)
	h.id = Identity{Names: []string{"me"}}
	unavailable := errors.New("synthetic disk error")
	var frozen *domain.Match
	h.tr.o.Save = func(v *domain.Match) error {
		if frozen == nil {
			frozen = cloneResult(v)
		} else if !reflect.DeepEqual(v, frozen) {
			t.Fatalf("failed record mutated: %+v", v)
		}
		v.GUID = "adapter-mutated"
		v.Me.PrimaryID = "changed"
		if len(v.Players) > 0 {
			v.Players[0].Name = "changed"
		}
		v.Statfeed["changed"] = 99
		return unavailable
	}
	g := G{Guid: "first", Players: []P{me, mt, op1, op2}}
	h.send("MatchCreated", map[string]any{"MatchGuid": g.Guid})
	g = h.playClock(g, 300, 250)
	h.send("MatchEnded", map[string]any{"MatchGuid": g.Guid, "WinnerTeamNum": 0})
	if h.tr.PendingRecords() != 1 || !errors.Is(h.tr.SaveError(), unavailable) {
		t.Fatal("failure not retained/observable")
	}
	// New match observations can continue, but cannot modify the failed result.
	h.advance(time.Minute)
	h.send("MatchCreated", map[string]any{"MatchGuid": "second"})
	h.update(G{Guid: "second", Players: []P{me, op1}, Time: 300})
	if err := h.tr.Flush(); !errors.Is(err, unavailable) {
		t.Fatal("shutdown did not report failed retry", err)
	}
	if h.tr.PendingRecords() != 1 {
		t.Fatal("pending result lost")
	}
	committed := 0
	h.tr.o.Save = func(v *domain.Match) error {
		if !reflect.DeepEqual(v, frozen) {
			t.Fatal("retry absorbed newer events")
		}
		committed++
		return nil
	}
	if err := h.tr.Flush(); err != nil {
		t.Fatal(err)
	}
	if committed != 1 || h.tr.PendingRecords() != 0 || h.tr.SaveError() != nil {
		t.Fatal("retry did not converge")
	}
	if err := h.tr.Flush(); err != nil || committed != 1 {
		t.Fatal("flush duplicated commit", err)
	}
}

func TestFailedTrainingRetainedAndRetryIsIndependent(t *testing.T) {
	h := newHarness(t)
	unavailable := errors.New("synthetic disk error")
	var frozen *domain.Training
	h.tr.o.SaveTraining = func(v *domain.Training) error {
		if frozen == nil {
			frozen = cloneTraining(v)
		} else if !reflect.DeepEqual(v, frozen) {
			t.Fatalf("training changed: %+v", v)
		}
		v.EndedAt = v.EndedAt.Add(time.Hour)
		v.Touches = 999
		return unavailable
	}
	solo := G{Players: []P{me}, Arena: "Park_P"}
	h.update(solo)
	h.advance(2 * time.Minute)
	h.update(solo)
	h.send("MatchDestroyed", map[string]any{})
	if h.tr.PendingRecords() != 1 || !errors.Is(h.tr.SaveError(), unavailable) {
		t.Fatal("failed training dropped")
	}
	h.advance(time.Hour)
	h.update(solo)
	if h.tr.PendingRecords() != 1 {
		t.Fatal("new observations lost pending training")
	}
	h.tr.o.SaveTraining = func(v *domain.Training) error {
		if !reflect.DeepEqual(v, frozen) {
			t.Fatal("retry extended training")
		}
		return nil
	}
	if err := h.tr.Flush(); err != nil || h.tr.PendingRecords() != 0 {
		t.Fatal("training retry failed", err)
	}
}

func TestPendingSnapshotsDeepCopyTelemetry(t *testing.T) {
	h := newHarness(t)
	sample := 1.0
	v := &domain.Match{Players: []domain.Player{{Name: "me"}}, Goals: []domain.Goal{{Impact: &domain.Vec{X: 1}}}, Movement: &domain.Movement{CarSampleS: &sample}, Hits: &domain.Hits{Points: []domain.HitPoint{{Pre: 2}}}, Statfeed: map[string]int{"Shot": 1}}
	frozen := cloneResult(v)
	h.tr.pending = []pendingRecord{{result: frozen}}
	sentinel := errors.New("try later")
	h.tr.o.Save = func(attempt *domain.Match) error {
		attempt.Goals[0].Impact.X = 99
		*attempt.Movement.CarSampleS = 99
		attempt.Hits.Points[0].Pre = 99
		attempt.Statfeed["Shot"] = 99
		return sentinel
	}
	if err := h.tr.Flush(); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(frozen, v) {
		t.Fatal("adapter mutated pending telemetry")
	}
	h.tr.o.Save = func(attempt *domain.Match) error {
		if !reflect.DeepEqual(attempt, v) {
			t.Fatal("retry telemetry changed")
		}
		return nil
	}
	if err := h.tr.Flush(); err != nil {
		t.Fatal(err)
	}
}

func TestPanickingSaveRetainsSnapshot(t *testing.T) {
	h := newHarness(t)
	h.tr.pending = []pendingRecord{{result: &domain.Match{GUID: "panic"}}}
	h.tr.o.Save = func(*domain.Match) error { panic("adapter panic") }
	if err := h.tr.Flush(); err == nil || h.tr.PendingRecords() != 1 {
		t.Fatal("panic lost record", err)
	}
	h.tr.o.Save = func(*domain.Match) error { return nil }
	if err := h.tr.Flush(); err != nil || h.tr.PendingRecords() != 0 {
		t.Fatal(err)
	}
}
