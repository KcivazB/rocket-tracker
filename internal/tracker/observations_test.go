package tracker

import (
	"encoding/json"
	"testing"
	"time"

	"rocket-tracker/internal/domain"
	"rocket-tracker/internal/statsapi"
)

func TestConfiguredSourceUnitsReachLiveMatchAndTraining(t *testing.T) {
	for _, unit := range []domain.SpeedUnit{domain.SpeedUnknown, domain.SpeedUUPerSecond, domain.SpeedKilometresPerHour} {
		t.Run(string(unit), func(t *testing.T) {
			h := newHarness(t)
			h.id = Identity{Names: []string{"Me"}}
			handle := statsapi.ObserveEventsWithOptions(h.tr.HandleEvent, statsapi.ObservationOptions{SpeedUnit: unit})
			send := func(name, data string) { handle(statsapi.Event{Name: name, Data: json.RawMessage(data)}) }
			state := func(guid string) {
				send("UpdateState", `{"MatchGuid":"`+guid+`","Players":[{"Name":"Me","PrimaryId":"id","TeamNum":0,"bHasCar":true,"bDemolished":false,"Speed":60,"Boost":0,"bOnGround":true,"bOnWall":false,"bBoosting":false,"bSupersonic":false,"bPowersliding":false}],"Game":{"TimeSeconds":300}}`)
			}
			state("X")
			h.advance(500 * time.Millisecond)
			state("X")
			send("BallHit", `{"Players":{"Name":"Me","TeamNum":0},"Ball":{"PostHitSpeed":90}}`)
			send("GoalScored", `{"Scorer":{"Name":"Me","TeamNum":0},"GoalSpeed":100}`)
			live := h.tr.Live()
			if live == nil || live.SpeedUnit != unit || live.Me == nil || live.Me.Movement == nil || live.Me.Movement.AvgSpeed != 60 || len(live.Goals) != 1 || live.Goals[0].Speed != 100 {
				t.Fatalf("live lost raw source unit/speeds: %+v", live)
			}
			send("MatchEnded", `{"WinnerTeamNum":0}`)
			m := h.only()
			if m.SpeedUnit != unit || m.Movement.AvgSpeed != 60 || m.Hits.AvgSpeed != 90 || m.Hits.MaxSpeed != 90 || m.Goals[0].Speed != 100 {
				t.Fatalf("match inferred conversion: %+v", m)
			}
			send("MatchCreated", `{}`)
			state("")
			send("BallHit", `{"Players":{"Name":"Me","TeamNum":0},"Ball":{"PostHitSpeed":90}}`)
			h.advance(2 * time.Minute)
			state("")
			send("MatchDestroyed", `{}`)
			if len(h.trainings) != 1 || h.trainings[0].SpeedUnit != unit || h.trainings[0].AvgHitSpeed != 90 || h.trainings[0].MaxHitSpeed != 90 {
				t.Fatalf("training inferred conversion: %+v", h.trainings)
			}
		})
	}
}

func TestSourceUnitChangeCannotMixMeasurements(t *testing.T) {
	h := newHarness(t)
	h.id = Identity{Names: []string{"Me"}}
	h.update(G{Guid: "UNITS", Players: []P{me, op1}, Time: 300})
	h.tr.HandleEvent(domain.Observation{Name: domain.EvGoalScored, SpeedUnit: domain.SpeedKilometresPerHour, Goal: &domain.GoalObservation{GoalSpeed: 90, Scorer: &domain.PlayerRef{Name: "Me"}}})
	live := h.tr.Live()
	if live == nil || live.SpeedUnit != domain.SpeedUnknown || len(live.Goals) != 0 {
		t.Fatalf("mixed incompatible source units: %+v", live)
	}
	for _, name := range []string{domain.EvUpdateState, domain.EvGoalScored, domain.EvClockUpdatedSeconds, domain.EvStatfeedEvent, domain.EvBallHit, domain.EvMatchEnded, "unknown"} {
		h.tr.HandleEvent(domain.Observation{Name: name})
	}
	if h.tr.Live() == nil || len(h.saved) != 0 {
		t.Fatal("absent typed payload changed lifecycle")
	}
}
