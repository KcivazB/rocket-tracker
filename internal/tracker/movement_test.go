package tracker

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"rocket-tracker/internal/domain"
)

func m03Ptr[T any](v T) *T { return &v }

func m03ObservedCar() domain.ObservedPlayer {
	return domain.ObservedPlayer{
		HasCar:       m03Ptr(bool(true)),
		Demolished:   m03Ptr(bool(false)),
		Speed:        m03Ptr(float64(1000)),
		Boost:        m03Ptr(float64(50)),
		OnGround:     m03Ptr(bool(true)),
		OnWall:       m03Ptr(bool(false)),
		Boosting:     m03Ptr(bool(false)),
		Supersonic:   m03Ptr(bool(false)),
		Powersliding: m03Ptr(bool(false)),
	}
}

func TestMovementIncompleteSampleHasNoResult(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    domain.ObservedPlayer
	}{
		{"no_details", domain.ObservedPlayer{}},
		{"has_car_only", domain.ObservedPlayer{HasCar: m03Ptr(bool(true))}},
		{"boost_only", domain.ObservedPlayer{Boost: m03Ptr(float64(50))}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var a movementAcc
			a.add(&tc.p, 0.5)
			if got := a.result(); got != nil {
				t.Fatalf("unobserved movement must remain unknown, got %+v", got)
			}
		})
	}
	for _, tc := range []struct {
		name string
		omit func(*domain.ObservedPlayer)
	}{
		{"has_car", func(p *domain.ObservedPlayer) { p.HasCar = nil }},
		{"demolished", func(p *domain.ObservedPlayer) { p.Demolished = nil }},
		{"speed", func(p *domain.ObservedPlayer) { p.Speed = nil }},
		{"boost", func(p *domain.ObservedPlayer) { p.Boost = nil }},
		{"on_ground", func(p *domain.ObservedPlayer) { p.OnGround = nil }},
		{"on_wall", func(p *domain.ObservedPlayer) { p.OnWall = nil }},
		{"boosting", func(p *domain.ObservedPlayer) { p.Boosting = nil }},
		{"supersonic", func(p *domain.ObservedPlayer) { p.Supersonic = nil }},
		{"powersliding", func(p *domain.ObservedPlayer) { p.Powersliding = nil }},
	} {
		t.Run("missing_"+tc.name, func(t *testing.T) {
			p := m03ObservedCar()
			tc.omit(&p)
			var a movementAcc
			a.add(&p, 0.5)
			if got := a.result(); got != nil {
				t.Fatalf("missing %s must not become a measured zero/false, got %+v", tc.name, got)
			}
		})
	}
}

func TestMovementExplicitZerosRemainMeasured(t *testing.T) {
	p := m03ObservedCar()
	p.Speed = m03Ptr(float64(0))
	p.Boost = m03Ptr(float64(0))
	var a movementAcc
	a.add(&p, 0.5)
	want := domain.Movement{CarSampleS: m03Ptr(0.5), SampleS: 0.5, GroundPct: 100, ZeroBoostPct: 100}
	if got := a.result(); !reflect.DeepEqual(got, &want) {
		t.Fatalf("explicit zero speed/boost and false states are measured: got %+v, want %+v", got, want)
	}
}

func TestMovementIncompleteSamplesDoNotDiluteKnown(t *testing.T) {
	ground := m03ObservedCar()
	air := m03ObservedCar()
	air.Speed = m03Ptr(float64(2000))
	air.Boost = m03Ptr(float64(100))
	air.OnGround = m03Ptr(bool(false))
	air.Boosting = m03Ptr(bool(true))
	air.Supersonic = m03Ptr(bool(true))
	air.Powersliding = m03Ptr(bool(true))
	incomplete := m03ObservedCar()
	incomplete.Speed = m03Ptr(float64(9999))
	incomplete.Boost = m03Ptr(float64(0))
	incomplete.OnGround = nil
	onlyCar := domain.ObservedPlayer{HasCar: m03Ptr(bool(true))}

	var a movementAcc
	a.add(&incomplete, 0.5)
	a.add(&ground, 0.5)
	a.add(&onlyCar, 0.5)
	a.add(&air, 0.5)
	a.add(&incomplete, 0.5)
	a.add(&ground, 0)
	a.add(&ground, -0.5)
	want := domain.Movement{
		CarSampleS: m03Ptr(1.0), SampleS: 1, AvgSpeed: 1500, AvgBoost: 75,
		GroundPct: 50, AirPct: 50, SupersonicPct: 50,
		FullBoostPct: 50, BoostingPct: 50, PowerslidePct: 50,
	}
	if got := a.result(); !reflect.DeepEqual(got, &want) {
		t.Fatalf("only the two observed car intervals contribute: got %+v, want %+v", got, want)
	}
}

func TestMovementObservedDemolitionsDoNotDiluteKnown(t *testing.T) {
	demo := domain.ObservedPlayer{HasCar: m03Ptr(bool(false)), Demolished: m03Ptr(bool(true))}
	ground := m03ObservedCar()
	noCar := domain.ObservedPlayer{HasCar: m03Ptr(bool(false)), Demolished: m03Ptr(bool(false))}
	var a movementAcc
	a.add(&demo, 0.5)
	a.add(&ground, 0.5)
	a.add(&noCar, 0.5)
	a.add(&demo, 0.5)
	want := domain.Movement{CarSampleS: m03Ptr(0.5), SampleS: 1.5, DemolishedS: 1, AvgSpeed: 1000, AvgBoost: 50, GroundPct: 100}
	if got := a.result(); !reflect.DeepEqual(got, &want) {
		t.Fatalf("observed demolition time is separate from car averages: got %+v, want %+v", got, want)
	}
}

func TestMovementWithoutObservedCarHasNoAverages(t *testing.T) {
	for _, tc := range []struct {
		name       string
		demolished bool
	}{{"no_car", false}, {"demolished_only", true}} {
		t.Run(tc.name, func(t *testing.T) {
			p := domain.ObservedPlayer{HasCar: m03Ptr(bool(false)), Demolished: m03Ptr(bool(tc.demolished))}
			var a movementAcc
			a.add(&p, 0.5)
			got := a.result()
			if !tc.demolished {
				if got != nil {
					t.Fatalf("unobserved car: %+v", got)
				}
			} else if got == nil || got.CarSampleS == nil || *got.CarSampleS != 0 || got.SampleS != 0.5 || got.DemolishedS != 0.5 {
				t.Fatalf("observed demolition duration was lost: %+v", got)
			}
		})
	}
}

func TestMovementIncompleteSamplesDoNotInferSpeedUnit(t *testing.T) {
	for _, tc := range []struct {
		name             string
		speed, wantSpeed float64
	}{
		{"low raw unknown", 60, 60},
		{"high raw unknown", 1500, 1500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.id = Identity{Names: []string{"Me"}}
			p := me
			p.Spec = map[string]any{
				"bHasCar": true, "bDemolished": false, "Speed": tc.speed, "Boost": 50,
				"bOnGround": true, "bOnWall": false, "bBoosting": false,
				"bSupersonic": false, "bPowersliding": false,
			}
			g := h.playClock(G{Guid: "M03" + tc.name, Players: []P{p, op1}}, 300, 250)
			incomplete := p
			incomplete.Spec = map[string]any{"bHasCar": true, "Speed": 9999, "Boost": 0, "bOnGround": true}
			g.Players[0] = incomplete
			g = h.playClock(g, 249, 245)
			g.Players[0] = p
			h.playClock(g, 244, 240)
			live := h.tr.Live()
			if live == nil || live.Me == nil || live.Me.Movement == nil || live.SpeedUnit != domain.SpeedUnknown || live.Me.Movement.AvgSpeed != tc.wantSpeed {
				t.Fatalf("live conversion changed for a complete packet: %+v", live)
			}
			h.send("MatchEnded", map[string]any{"WinnerTeamNum": 0})
			if m := h.only(); m.Movement == nil || m.SpeedUnit != domain.SpeedUnknown || m.Movement.AvgSpeed != tc.wantSpeed {
				t.Fatalf("stored conversion changed for a complete packet: %+v", m.Movement)
			}
		})
	}
}

func TestDemolitionOnlySurvivesTransportLiveAndSave(t *testing.T) {
	h := newHarness(t)
	h.id = Identity{Names: []string{"Me"}}
	p := me
	p.Spec = map[string]any{"bDemolished": true} // no fields used by the identity heuristic
	g := G{Guid: "DEMO-ONLY", Players: []P{p, op1}, Time: 300}
	h.update(g)
	h.advance(500 * time.Millisecond)
	g.Time = 299
	h.update(g)
	live := h.tr.Live()
	if live == nil || live.Me == nil || live.Me.Movement == nil || live.Me.Movement.CarSampleS == nil || *live.Me.Movement.CarSampleS != 0 || live.Me.Movement.DemolishedS != 0.5 {
		t.Fatalf("live lost demolition-only data: %+v", live)
	}
	p.Spec = nil
	g.Players[0] = p
	h.advance(500 * time.Millisecond)
	g.Time = 298
	h.update(g)
	h.send("MatchEnded", map[string]any{"WinnerTeamNum": 0})
	m := h.only()
	if m.Movement == nil || m.Movement.CarSampleS == nil || *m.Movement.CarSampleS != 0 || m.Movement.DemolishedS != 0.5 || m.Movement.SampleS != 0.5 {
		t.Fatalf("saved record lost demolition-only data: %+v", m.Movement)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Movement map[string]json.RawMessage `json:"movement"`
	}
	if err := json.Unmarshal(b, &data); err != nil {
		t.Fatal(err)
	}
	if string(data.Movement["avg_speed"]) != "null" || string(data.Movement["air_pct"]) != "null" || string(data.Movement["demolished_s"]) != "0.5" {
		t.Fatalf("invented car metrics: %s", b)
	}
}
