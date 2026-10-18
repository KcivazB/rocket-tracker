package domain_test

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"rocket-tracker/internal/domain"
)

func TestTrainingRulesAndJSONContract(t *testing.T) {
	const fixture = `{"id":4,"started_at":"2026-10-18T12:00:00Z","ended_at":"2026-10-18T12:10:00Z","source":"timer","focus":"shooting","profile":"epic|synthetic|0","arena":"Stadium_P","touches":12,"avg_hit_speed":1500,"max_hit_speed":3000}`
	var tr domain.Training
	if err := json.Unmarshal([]byte(fixture), &tr); err != nil {
		t.Fatal(err)
	}
	tr.Focus, tr.Profile = " Shooting ", " Epic|Synthetic|0 "
	tr.StartedAt = tr.StartedAt.In(time.FixedZone("offset", 7200)).Add(800 * time.Millisecond)
	if err := tr.Normalize(); err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(tr)
	if err != nil || string(got) != fixture {
		t.Fatalf("training JSON changed: %s (%v)", got, err)
	}
	before := tr
	if err := tr.Normalize(); err != nil || !reflect.DeepEqual(tr, before) {
		t.Fatalf("Normalize not idempotent: %+v (%v)", tr, err)
	}
	tr.Focus = "unknown"
	if err := tr.Normalize(); !errors.Is(err, domain.ErrInvalidTraining) || err.Error() != `store: invalid training: unknown focus "unknown"` {
		t.Fatalf("error identity/text changed: %v", err)
	}
}

func TestTrainingMergePreservesEditsAndReplay(t *testing.T) {
	start := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	tr := domain.Training{ID: 7, StartedAt: start, EndedAt: start.Add(time.Minute), Source: "timer", Focus: "aerials", Profile: "chosen", Arena: "chosen", Touches: 10, AvgHitSpeed: 1200, MaxHitSpeed: 3000}
	other := domain.Training{StartedAt: start.Add(-time.Minute), EndedAt: start.Add(2 * time.Minute), Source: "game", Focus: "shooting", Profile: "incoming", Arena: "incoming", Touches: 20, AvgHitSpeed: 1400, MaxHitSpeed: 2500}
	tr.Merge(&other)
	want := domain.Training{ID: 7, StartedAt: other.StartedAt, EndedAt: other.EndedAt, Source: "game", Focus: "aerials", Profile: "chosen", Arena: "chosen", Touches: 20, AvgHitSpeed: 1400, MaxHitSpeed: 3000}
	if !reflect.DeepEqual(tr, want) {
		t.Fatalf("merge replaced edits or lost metrics: %+v", tr)
	}
	tr.Merge(&other)
	if !reflect.DeepEqual(tr, want) {
		t.Fatalf("replay changed merged training: %+v", tr)
	}
}

func TestTrainingRejectsNonFiniteSpeeds(t *testing.T) {
	start := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		for _, field := range []string{"average", "maximum"} {
			tr := domain.Training{StartedAt: start, EndedAt: start.Add(time.Minute), Source: "game"}
			if field == "average" {
				tr.AvgHitSpeed = v
			} else {
				tr.MaxHitSpeed = v
			}
			if err := tr.Normalize(); !errors.Is(err, domain.ErrInvalidTraining) {
				t.Fatalf("accepted nonfinite %s: %v", field, err)
			}
		}
	}
}

func TestTrainingMergeSpeedUnits(t *testing.T) {
	start := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	for _, unit := range []domain.SpeedUnit{"", domain.SpeedUUPerSecond, domain.SpeedKilometresPerHour, domain.SpeedUnknown} {
		tr := domain.Training{StartedAt: start, EndedAt: start.Add(time.Minute), Source: "game", Touches: 10, AvgHitSpeed: 20, MaxHitSpeed: 40, SpeedUnit: unit}
		larger := domain.Training{StartedAt: start, EndedAt: start.Add(2 * time.Minute), Source: "game", Touches: 20, AvgHitSpeed: 30, MaxHitSpeed: 50, SpeedUnit: unit}
		tr.Merge(&larger)
		if tr.Touches != 20 || tr.AvgHitSpeed != 30 || tr.MaxHitSpeed != 50 || tr.SpeedUnit != unit {
			t.Fatalf("same-unit merge degraded: %+v", tr)
		}
		smaller := larger
		smaller.Touches = 5
		smaller.AvgHitSpeed = 100
		smaller.MaxHitSpeed = 60
		tr.Merge(&smaller)
		if tr.Touches != 20 || tr.AvgHitSpeed != 30 || tr.MaxHitSpeed != 60 || tr.SpeedUnit != unit {
			t.Fatalf("smaller sample replaced average: %+v", tr)
		}
	}
	tr := domain.Training{StartedAt: start, EndedAt: start.Add(time.Minute), Source: "game", Focus: "aerials", Touches: 10, AvgHitSpeed: 1500, MaxHitSpeed: 3000}
	incoming := domain.Training{StartedAt: start, EndedAt: start.Add(2 * time.Minute), Source: "game", Touches: 20, AvgHitSpeed: 60, MaxHitSpeed: 100, SpeedUnit: domain.SpeedUnknown}
	tr.Merge(&incoming)
	if tr.Touches != 20 || tr.AvgHitSpeed != 1500 || tr.MaxHitSpeed != 3000 || tr.SpeedUnit != "" || tr.Focus != "aerials" || !tr.EndedAt.Equal(incoming.EndedAt) {
		t.Fatalf("incompatible units were compared: %+v", tr)
	}
	timer := domain.Training{StartedAt: start, EndedAt: start.Add(time.Minute), Source: "timer", Focus: "shooting"}
	timer.Merge(&incoming)
	if timer.Touches != 20 || timer.AvgHitSpeed != 60 || timer.MaxHitSpeed != 100 || timer.SpeedUnit != domain.SpeedUnknown || timer.Focus != "shooting" {
		t.Fatalf("empty timer lost game telemetry: %+v", timer)
	}
}
