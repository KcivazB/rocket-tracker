package domain_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"rocket-tracker/internal/domain"
)

func TestTrainingActiveIntervalsCompatibility(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, field := range []string{"", `,"active_intervals":null`, `,"active_intervals":[]`} {
		var v domain.Training
		payload := `{"started_at":"2026-01-01T00:00:00Z","ended_at":"2026-01-01T00:10:00Z","source":"timer"` + field + `}`
		if err := json.Unmarshal([]byte(payload), &v); err != nil {
			t.Fatal(err)
		}
		if err := v.Normalize(); err != nil {
			t.Fatal(err)
		}
		want := 10 * time.Minute
		if strings.HasSuffix(field, "[]") {
			want = 0
		}
		if v.ActiveDuration() != want {
			t.Fatalf("%s: duration %s", field, v.ActiveDuration())
		}
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var copy domain.Training
		if err := json.Unmarshal(b, &copy); err != nil {
			t.Fatal(err)
		}
		if copy.ActiveDuration() != want {
			t.Fatalf("roundtrip changed presence: %s", b)
		}
	}
	v := domain.Training{StartedAt: at, EndedAt: at.Add(10 * time.Minute), Source: "timer", ActiveIntervals: []domain.TrainingInterval{
		{StartedAt: at.Add(5 * time.Minute), EndedAt: at.Add(7 * time.Minute)},
		{StartedAt: at.Add(time.Minute), EndedAt: at.Add(3 * time.Minute)},
		{StartedAt: at.Add(2 * time.Minute), EndedAt: at.Add(4 * time.Minute)},
	}}
	if err := v.Normalize(); err != nil {
		t.Fatal(err)
	}
	if len(v.ActiveIntervals) != 2 || v.ActiveDuration() != 5*time.Minute {
		t.Fatalf("not unioned: %+v", v)
	}
	before := v
	before.ActiveIntervals = append([]domain.TrainingInterval{}, v.ActiveIntervals...)
	if err := v.Normalize(); err != nil || !reflect.DeepEqual(before, v) {
		t.Fatalf("not idempotent: %+v %v", v, err)
	}
	replay := v
	v.Merge(&replay)
	if !reflect.DeepEqual(before, v) {
		t.Fatalf("replay changed activity: %+v", v)
	}
	legacy := domain.Training{StartedAt: at.Add(11 * time.Minute), EndedAt: at.Add(12 * time.Minute), Source: "game"}
	v.Merge(&legacy)
	if v.ActiveDuration() != 6*time.Minute {
		t.Fatalf("pause/proximity gap counted: %+v", v)
	}
}

func TestTrainingRejectsInvalidActiveIntervals(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, intervals := range [][]domain.TrainingInterval{
		{{StartedAt: at.Add(-time.Nanosecond), EndedAt: at}},
		{{StartedAt: at, EndedAt: at.Add(10*time.Minute + time.Nanosecond)}},
		{{StartedAt: at.Add(time.Minute), EndedAt: at}},
		{{EndedAt: at}},
		make([]domain.TrainingInterval, domain.MaxTrainingIntervals+1),
	} {
		v := domain.Training{StartedAt: at, EndedAt: at.Add(10 * time.Minute), Source: "timer", ActiveIntervals: intervals}
		if err := v.Normalize(); err == nil {
			t.Fatalf("accepted invalid intervals: %+v", intervals[:1])
		}
	}
	a := domain.Training{StartedAt: at, EndedAt: at.Add(domain.MaxTraining), Source: "game"}
	b := domain.Training{StartedAt: a.EndedAt, EndedAt: a.EndedAt.Add(time.Second), Source: "game"}
	if a.CanMerge(&b) {
		t.Fatal("12h overflow allowed")
	}
	before := a
	a.Merge(&b)
	if !reflect.DeepEqual(a, before) {
		t.Fatal("overflow mutated record")
	}
}

func TestHistoricalZeroDurationMergeDoesNotFillGap(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := domain.Training{StartedAt: at, EndedAt: at, Source: "timer"}
	b := domain.Training{StartedAt: at.Add(time.Minute), EndedAt: at.Add(2 * time.Minute), Source: "game"}
	a.Merge(&b)
	if a.ActiveDuration() != time.Minute {
		t.Fatal("zero-span historical record filled inactive gap", a)
	}
}
