package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestAgentMovementAvailabilityIngestion(t *testing.T) {
	e := newHub(t, nil)
	ctx := context.Background()
	u, err := e.st.UpsertOIDCUser(ctx, "measurement-sub", "measurement", "Measurement", "")
	if err != nil {
		t.Fatal(err)
	}
	scope := e.st.User(u.ID)
	_, token, err := scope.CreateDevice(ctx, "measurement fixture")
	if err != nil {
		t.Fatal(err)
	}
	auth := []string{"Authorization", "Bearer " + token, "Origin", ""}
	client := e.ts.Client()
	endpoint := e.ts.URL + "/api/agent/matches"
	keys := []string{"avg_speed", "supersonic_pct", "ground_pct", "wall_pct", "air_pct", "avg_boost", "zero_boost_pct", "full_boost_pct", "boosting_pct", "powerslide_pct"}
	complete := func() map[string]any {
		m := map[string]any{"car_sample_s": 1, "sample_s": 1}
		for _, key := range keys {
			m[key] = 0
		}
		return m
	}
	upload := func(t *testing.T, guid string, score int, movement map[string]any, authenticated bool) (int, string) {
		t.Helper()
		payload, err := json.Marshal(map[string]any{
			"key": guid,
			"match": map[string]any{
				"guid": guid, "started_at": "2026-10-17T10:00:00Z", "ended_at": "2026-10-17T10:05:00Z",
				"mode": "1v1", "result": "win", "team_score": score, "opp_score": 0,
				"me": map[string]any{"name": "Measurement", "id": "Epic|measurement|0"}, "movement": movement,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if authenticated {
			return call(t, client, http.MethodPost, endpoint, string(payload), auth...)
		}
		return call(t, client, http.MethodPost, endpoint, string(payload), "Origin", "")
	}
	snapshot := func(t *testing.T) string {
		t.Helper()
		matches, err := scope.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(matches)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if code, body := upload(t, "existing", 1, complete(), true); code != http.StatusOK {
		t.Fatalf("seed full measured zeros: %d %s", code, body)
	}
	before, rev := snapshot(t), scope.DataRev()
	assertNoWrite := func(t *testing.T) {
		t.Helper()
		if got := snapshot(t); got != before {
			t.Fatalf("rejected upload changed stored matches: %s", got)
		}
		if got := scope.DataRev(); got != rev {
			t.Fatalf("rejected upload changed revision: %d -> %d", rev, got)
		}
	}
	t.Run("requires device authentication", func(t *testing.T) {
		if code, _ := upload(t, "unauthenticated", 9, complete(), false); code != http.StatusUnauthorized {
			t.Fatalf("without token: %d", code)
		}
		assertNoWrite(t)
	})
	t.Run("reported null averages", func(t *testing.T) {
		movement := map[string]any{"car_sample_s": 1, "sample_s": 1, "avg_speed": nil, "avg_boost": nil}
		for _, guid := range []string{"new", "existing"} {
			if code, body := upload(t, guid, 9, movement, true); code != http.StatusBadRequest {
				t.Fatalf("incomplete movement accepted for %s: %d %s", guid, code, body)
			}
			assertNoWrite(t)
		}
	})
	for _, key := range keys {
		for _, absent := range []bool{false, true} {
			name := key + "/null"
			if absent {
				name = key + "/absent"
			}
			t.Run(name, func(t *testing.T) {
				movement := complete()
				movement[key] = nil
				if absent {
					delete(movement, key)
				}
				for _, guid := range []string{"new", "existing"} {
					if code, body := upload(t, guid, 9, movement, true); code != http.StatusBadRequest {
						t.Fatalf("incomplete movement accepted for %s: %d %s", guid, code, body)
					}
					assertNoWrite(t)
				}
			})
		}
	}
	for _, tc := range []struct {
		name     string
		movement map[string]any
		carTime  string
		unknown  bool
	}{
		{"legacy absent", map[string]any{"sample_s": 1, "avg_speed": nil, "avg_boost": nil}, "", false},
		{"legacy null", map[string]any{"car_sample_s": nil, "sample_s": 1}, "", false},
		{"demolition only", map[string]any{"car_sample_s": 0, "sample_s": 1, "demolished_s": 1, "avg_speed": nil, "avg_boost": nil}, "0", true},
		{"complete measured zeros", complete(), "1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code, body := upload(t, tc.name, 1, tc.movement, true); code != http.StatusOK {
				t.Fatalf("compatible movement rejected: %d %s", code, body)
			}
			matches, err := scope.List(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range matches {
				if m.GUID != tc.name {
					continue
				}
				b, err := json.Marshal(m.Movement)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(b, &fields); err != nil {
					t.Fatal(err)
				}
				if string(fields["car_sample_s"]) != tc.carTime || string(fields["sample_s"]) != "1" {
					t.Fatalf("lost car availability or duration: %s", b)
				}
				for _, key := range keys {
					want := "0"
					if tc.unknown {
						want = "null"
					}
					if string(fields[key]) != want {
						t.Fatalf("%s: got %s, want %s", key, fields[key], want)
					}
				}
				if tc.unknown && string(fields["demolished_s"]) != "1" {
					t.Fatalf("lost demolition observation: %s", b)
				}
				return
			}
			t.Fatal("accepted match not stored")
		})
	}
}
