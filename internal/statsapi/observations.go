package statsapi

import (
	"fmt"
	"rocket-tracker/internal/domain"
)

// ObservationOptions declares the unit of every speed from this source.
// Unknown (the default) preserves raw values; no amplitude inference or unit
// conversion is performed. Keep this configuration fixed for a tracker stream.
type ObservationOptions struct{ SpeedUnit domain.SpeedUnit }

// ObserveEvents adapts a transport callback to the pure tracker input. It keeps
// the established decoder policy: malformed UpdateState is discarded; other
// events retain their decoded fields (or zero payload on invalid syntax).
func ObserveEvents(handle func(domain.Observation)) func(Event) {
	return ObserveEventsWithOptions(handle, ObservationOptions{})
}

func ObserveEventsWithOptions(handle func(domain.Observation), options ObservationOptions) func(Event) {
	return func(ev Event) {
		observation, err := ToObservationWithOptions(ev, options)
		if err == nil {
			handle(observation)
		}
	}
}

// ToObservation maps only the facts consumed by the tracker. Transport DTOs and
// tolerant scalar decoding stay here; optional values are copied, not defaulted.
func ToObservation(ev Event) (domain.Observation, error) {
	return ToObservationWithOptions(ev, ObservationOptions{})
}

func ToObservationWithOptions(ev Event, options ObservationOptions) (domain.Observation, error) {
	unit := options.SpeedUnit
	if !unit.Valid() {
		return domain.Observation{}, fmt.Errorf("statsapi: unknown source speed unit %q", unit)
	}
	if unit == "" {
		unit = domain.SpeedUnknown
	}
	o := domain.Observation{Name: ev.Name, SpeedUnit: unit}
	switch ev.Name {
	case EvMatchCreated, EvMatchInitialized, EvMatchDestroyed:
		var d MatchGUIDOnly
		_ = ev.Decode(&d)
		o.MatchGUID = string(d.MatchGUID)
	case EvUpdateState:
		var d UpdateState
		if err := ev.Decode(&d); err != nil {
			return domain.Observation{}, err
		}
		s := &domain.StateObservation{MatchGUID: string(d.MatchGUID)}
		for _, p := range d.Players {
			s.Players = append(s.Players, observePlayer(p))
		}
		if g := d.Game; g != nil {
			s.Game = &domain.ObservedGame{TimeSeconds: observeInt(g.TimeSeconds), Overtime: bool(g.Overtime), Replay: bool(g.Replay), HasTarget: bool(g.HasTarget), Arena: string(g.Arena), Target: observeRef(g.Target)}
			for _, t := range g.Teams {
				s.Game.Teams = append(s.Game.Teams, domain.TeamScore{TeamNum: int(t.TeamNum), Score: int(t.Score)})
			}
		}
		o.State = s
	case EvClockUpdatedSeconds:
		var d ClockUpdated
		_ = ev.Decode(&d)
		o.Clock = &domain.ClockObservation{MatchGUID: string(d.MatchGUID), TimeSeconds: observeInt(d.TimeSeconds), Overtime: bool(d.Overtime)}
	case EvGoalScored:
		var d GoalScored
		_ = ev.Decode(&d)
		o.Goal = &domain.GoalObservation{MatchGUID: string(d.MatchGUID), GoalSpeed: float64(d.GoalSpeed), ImpactLocation: observePosition(d.ImpactLocation), Scorer: observeRef(d.Scorer), Assister: observeRef(d.Assister)}
		if d.BallLastTouch != nil {
			o.Goal.LastTouch = observeRef(d.BallLastTouch.Player)
		}
	case EvStatfeedEvent:
		var d StatfeedEvent
		_ = ev.Decode(&d)
		o.Feed = &domain.FeedObservation{MatchGUID: string(d.MatchGUID), EventName: string(d.EventName), MainTarget: observeRef(d.MainTarget), SecondaryTarget: observeRef(d.SecondaryTarget)}
	case EvBallHit:
		var d BallHit
		_ = ev.Decode(&d)
		o.Hit = &domain.HitObservation{MatchGUID: string(d.MatchGUID), Player: observeRef(d.Player)}
		for i := range d.Players {
			o.Hit.Players = append(o.Hit.Players, *observeRef(&d.Players[i]))
		}
		if b := d.Ball; b != nil {
			o.Hit.Ball = &domain.ObservedHitBall{PreHitSpeed: float64(b.PreHitSpeed), PostHitSpeed: float64(b.PostHitSpeed), Location: observePosition(b.Location)}
		}
	case EvMatchEnded:
		var d MatchEnded
		_ = ev.Decode(&d)
		o.End = &domain.EndObservation{MatchGUID: string(d.MatchGUID), WinnerTeamNum: observeInt(d.WinnerTeamNum)}
	}
	return o, nil
}

func observePlayer(p Player) domain.ObservedPlayer {
	return domain.ObservedPlayer{
		Name: string(p.Name), PrimaryID: string(p.PrimaryID), Shortcut: int(p.Shortcut), TeamNum: int(p.TeamNum), Score: int(p.Score), Goals: int(p.Goals), Shots: int(p.Shots), Assists: int(p.Assists), Saves: int(p.Saves), Touches: int(p.Touches), Demos: int(p.Demos),
		HasCar: observeBool(p.HasCar), Speed: observeNum(p.Speed), Boost: observeNum(p.Boost), Boosting: observeBool(p.Boosting), OnGround: observeBool(p.OnGround), OnWall: observeBool(p.OnWall), Powersliding: observeBool(p.Powersliding), Demolished: observeBool(p.Demolished), Supersonic: observeBool(p.Supersonic),
	}
}
func observeRef(p *PlayerRef) *domain.PlayerRef {
	if p == nil {
		return nil
	}
	return &domain.PlayerRef{Name: string(p.Name), Shortcut: int(p.Shortcut), TeamNum: int(p.TeamNum)}
}
func observePosition(p *Vec3) *domain.Position {
	if p == nil {
		return nil
	}
	return &domain.Position{X: float64(p.X), Y: float64(p.Y), Z: float64(p.Z)}
}
func observeBool(p *Bool) *bool {
	if p == nil {
		return nil
	}
	v := bool(*p)
	return &v
}
func observeNum(p *Num) *float64 {
	if p == nil {
		return nil
	}
	v := float64(*p)
	return &v
}
func observeInt(p *Int) *int {
	if p == nil {
		return nil
	}
	v := int(*p)
	return &v
}
