package domain_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"rocket-tracker/internal/domain"
)

func TestMovementAvailabilityJSONRoundTrip(t *testing.T) {
	zero, half := 0.0, 0.5
	for _, tc := range []struct {
		name     string
		movement domain.Movement
		unknown  bool
	}{
		{"legacy numeric zero", domain.Movement{}, false},
		{"measured zero", domain.Movement{CarSampleS: &half, SampleS: 0.5, GroundPct: 100, ZeroBoostPct: 100}, false},
		{"demolition only", domain.Movement{CarSampleS: &zero, SampleS: 0.5, DemolishedS: 0.5}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.movement)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(b, &fields); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"avg_speed", "supersonic_pct", "ground_pct", "wall_pct", "air_pct", "avg_boost", "zero_boost_pct", "full_boost_pct", "boosting_pct", "powerslide_pct"} {
				got, present := fields[key]
				if !present || (string(got) == "null") != tc.unknown {
					t.Fatalf("availability %s: %s", key, b)
				}
			}
			if tc.unknown && (string(fields["car_sample_s"]) != "0" || string(fields["demolished_s"]) != "0.5") {
				t.Fatalf("lost observed duration: %s", b)
			}
			if tc.movement.CarSampleS == nil {
				if _, ok := fields["car_sample_s"]; ok {
					t.Fatalf("legacy schema changed: %s", b)
				}
			}
			var restored domain.Movement
			if err := json.Unmarshal(b, &restored); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(restored, tc.movement) {
				t.Fatalf("round trip: %+v", restored)
			}
			again, err := json.Marshal(restored)
			if err != nil || string(b) != string(again) {
				t.Fatalf("availability lost after storage: %s / %s (%v)", b, again, err)
			}
		})
	}
}

func TestMovementDecodeRequiresCompleteMeasuredCar(t *testing.T) {
	keys := []string{"avg_speed", "supersonic_pct", "ground_pct", "wall_pct", "air_pct", "avg_boost", "zero_boost_pct", "full_boost_pct", "boosting_pct", "powerslide_pct"}
	complete := `{"car_sample_s":1,"sample_s":1,"avg_speed":0,"supersonic_pct":0,"ground_pct":0,"wall_pct":0,"air_pct":0,"avg_boost":0,"zero_boost_pct":0,"full_boost_pct":0,"boosting_pct":0,"powerslide_pct":0}`
	var measured domain.Movement
	if err := json.Unmarshal([]byte(complete), &measured); err != nil {
		t.Fatalf("explicit measured zeros rejected: %v", err)
	}
	if measured.CarSampleS == nil || *measured.CarSampleS != 1 || measured.AvgSpeed != 0 || measured.AvgBoost != 0 {
		t.Fatalf("measured zeros changed: %+v", measured)
	}
	for _, key := range keys {
		for _, absent := range []bool{false, true} {
			name := key + "/null"
			if absent {
				name = key + "/absent"
			}
			t.Run(name, func(t *testing.T) {
				var fields map[string]json.RawMessage
				if err := json.Unmarshal([]byte(complete), &fields); err != nil {
					t.Fatal(err)
				}
				fields[key] = json.RawMessage("null")
				if absent {
					delete(fields, key)
				}
				b, err := json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
				original := domain.Movement{SampleS: 7, AvgSpeed: 42}
				got := original
				if err := json.Unmarshal(b, &got); err == nil {
					t.Fatalf("incomplete measured car accepted: %s", b)
				}
				if !reflect.DeepEqual(got, original) {
					t.Fatalf("failed decode mutated destination: %+v", got)
				}
			})
		}
	}
}

func TestMovementDecodeToleratesUnavailableAndLegacyCar(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		car  *float64
	}{
		{"legacy absent", `{"sample_s":1,"avg_speed":null,"avg_boost":null}`, nil},
		{"legacy null", `{"car_sample_s":null,"sample_s":1}`, nil},
		{"demolition null aggregates", `{"car_sample_s":0,"sample_s":1,"avg_speed":null,"avg_boost":null,"demolished_s":1}`, new(float64)},
		{"demolition absent aggregates", `{"car_sample_s":0,"sample_s":1,"demolished_s":1}`, new(float64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got domain.Movement
			if err := json.Unmarshal([]byte(tc.body), &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.CarSampleS, tc.car) || got.SampleS != 1 {
				t.Fatalf("lost availability or time: %+v", got)
			}
			if tc.car != nil && got.DemolishedS != 1 {
				t.Fatalf("lost demolition: %+v", got)
			}
		})
	}
}
