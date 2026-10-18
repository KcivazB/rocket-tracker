package statsapi

import (
	"encoding/json"
	"testing"

	"rocket-tracker/internal/domain"
)

func TestObservationMappingPreservesPresenceAndTolerantScalars(t *testing.T) {
	ev, err := DecodeEnvelope([]byte(`{"Event":"UpdateState","Data":"{\"MatchGuid\":\"X\",\"Players\":[{\"Name\":\"Me\",\"Speed\":\"0\",\"Boost\":null,\"bHasCar\":\"false\",\"Score\":\"12\"}],\"Game\":{\"TimeSeconds\":\"0\",\"Target\":{\"Name\":\"Me\",\"TeamNum\":1}}}"}`))
	if err != nil {
		t.Fatal(err)
	}
	o, err := ToObservation(ev)
	if err != nil {
		t.Fatal(err)
	}
	if o.Name != domain.EvUpdateState || o.State == nil || o.State.MatchGUID != "X" || len(o.State.Players) != 1 {
		t.Fatalf("bad state mapping: %+v", o)
	}
	p := o.State.Players[0]
	if p.Name != "Me" || p.Score != 12 || p.Speed == nil || *p.Speed != 0 || p.HasCar == nil || *p.HasCar || p.Boost != nil || p.Demolished != nil {
		t.Fatalf("presence/coercion lost: %+v", p)
	}
	if o.State.Game.TimeSeconds == nil || *o.State.Game.TimeSeconds != 0 || o.State.Game.Target.TeamNum != 1 {
		t.Fatalf("clock/reference mapping: %+v", o.State.Game)
	}
	raw := Player{Speed: new(Num), HasCar: new(Bool)}
	copied := observePlayer(raw)
	*raw.Speed, *raw.HasCar = 99, true
	if *copied.Speed != 0 || *copied.HasCar {
		t.Fatal("observation retains mutable transport pointers")
	}
}

func TestObservationMappingHitReferencesAndPositions(t *testing.T) {
	for _, players := range []string{`{"Name":"Me","Shortcut":"2","TeamNum":1}`, `[{"Name":"Me","Shortcut":"2","TeamNum":1}]`} {
		o, err := ToObservation(Event{Name: EvBallHit, Data: json.RawMessage(`{"MatchGuid":"X","Players":` + players + `,"Ball":{"PreHitSpeed":"0","PostHitSpeed":2400,"Location":{"X":-123,"Y":5120,"Z":93}}}`)})
		if err != nil || o.Hit == nil || len(o.Hit.Players) != 1 || o.Hit.Players[0].Name != "Me" || o.Hit.Players[0].Shortcut != 2 || o.Hit.Ball.PreHitSpeed != 0 || o.Hit.Ball.PostHitSpeed != 2400 || *o.Hit.Ball.Location != (domain.Position{X: -123, Y: 5120, Z: 93}) {
			t.Fatalf("lost singleton/array reference or position: %+v (%v)", o.Hit, err)
		}
	}
}

func TestObservationAdapterKeepsDecoderPolicy(t *testing.T) {
	var got []domain.Observation
	handle := ObserveEvents(func(o domain.Observation) { got = append(got, o) })
	handle(Event{Name: EvUpdateState, Data: json.RawMessage(`invalid`)})
	if len(got) != 0 {
		t.Fatal("malformed state reached tracker")
	}
	handle(Event{Name: EvMatchEnded, Data: json.RawMessage(`invalid`)})
	handle(Event{Name: EvUpdateState, Data: json.RawMessage(`{"Players":"wrong type","Game":{"TimeSeconds":"300"}}`)})
	handle(Event{Name: "unknown"})
	if len(got) != 3 || got[0].End == nil || got[0].End.WinnerTeamNum != nil || got[1].State.Game.TimeSeconds == nil || *got[1].State.Game.TimeSeconds != 300 || got[2].Name != "unknown" {
		t.Fatalf("legacy tolerance changed: %+v", got)
	}
}

func TestObservationExplicitSourceUnitDoesNotConvert(t *testing.T) {
	for _, unit := range []domain.SpeedUnit{domain.SpeedUnknown, domain.SpeedUUPerSecond, domain.SpeedKilometresPerHour} {
		o, err := ToObservationWithOptions(Event{Name: EvGoalScored, Data: json.RawMessage(`{"GoalSpeed":60}`)}, ObservationOptions{SpeedUnit: unit})
		if err != nil || o.SpeedUnit != unit || o.Goal.GoalSpeed != 60 {
			t.Fatalf("configured raw speed changed: %+v (%v)", o, err)
		}
	}
	if o, err := ToObservation(Event{Name: EvGoalScored, Data: json.RawMessage(`{"GoalSpeed":60}`)}); err != nil || o.SpeedUnit != domain.SpeedUnknown || o.Goal.GoalSpeed != 60 {
		t.Fatalf("default invented a source unit: %+v (%v)", o, err)
	}
	if _, err := ToObservationWithOptions(Event{Name: EvGoalScored}, ObservationOptions{SpeedUnit: "guessed"}); err == nil {
		t.Fatal("invalid source contract accepted")
	}
}
