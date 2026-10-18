package tracker

import (
	"rocket-tracker/internal/domain"
)

// movementAcc integrates spectator car-state samples over real time.
type movementAcc struct {
	total      float64 // accepted car seconds plus observed demolition seconds
	car        float64 // seconds with complete live-car data (base for percentages)
	speed      float64 // integral of speed
	supersonic float64
	ground     float64
	wall       float64
	air        float64
	boost      float64 // integral of boost amount
	zeroBoost  float64
	fullBoost  float64
	boosting   float64
	powerslide float64
	demolished float64
}

func (a *movementAcc) add(p *domain.ObservedPlayer, dt float64) {
	if dt <= 0 {
		return
	}
	if p.Demolished != nil && bool(*p.Demolished) {
		a.total += dt
		a.demolished += dt
		return
	}
	// All car metrics share one denominator. Require a complete live-car
	// packet so omitted fields cannot become measured zero/false or dilute data.
	if p.HasCar == nil || !bool(*p.HasCar) || p.Demolished == nil ||
		p.Speed == nil || p.Boost == nil || p.OnGround == nil || p.OnWall == nil ||
		p.Boosting == nil || p.Supersonic == nil || p.Powersliding == nil {
		return
	}
	a.total += dt
	a.car += dt
	a.speed += float64(*p.Speed) * dt
	if bool(*p.Supersonic) {
		a.supersonic += dt
	}
	// The game sets bOnGround whenever the wheels touch any surface (floor,
	// walls, ceiling) and additionally sets bOnWall on walls/ceiling, so the
	// wall test must come first or wall time is counted as ground time.
	switch {
	case bool(*p.OnWall):
		a.wall += dt
	case bool(*p.OnGround):
		a.ground += dt
	default:
		a.air += dt
	}
	b := float64(*p.Boost)
	a.boost += b * dt
	if b <= 0.5 {
		a.zeroBoost += dt
	}
	if b >= 99.5 {
		a.fullBoost += dt
	}
	if bool(*p.Boosting) {
		a.boosting += dt
	}
	if bool(*p.Powersliding) {
		a.powerslide += dt
	}
}

func (a *movementAcc) result() *domain.Movement {
	if a.total <= 0 {
		return nil
	}
	car := a.car
	m := &domain.Movement{SampleS: round1(a.total), CarSampleS: &car, DemolishedS: round1(a.demolished)}
	if a.car <= 0 {
		return m
	}
	pct := func(x float64) float64 { return round1(100 * x / a.car) }
	m.AvgSpeed = round1(a.speed / a.car)
	m.SupersonicPct = pct(a.supersonic)
	m.GroundPct = pct(a.ground)
	m.WallPct = pct(a.wall)
	m.AirPct = pct(a.air)
	m.AvgBoost = round1(a.boost / a.car)
	m.ZeroBoostPct = pct(a.zeroBoost)
	m.FullBoostPct = pct(a.fullBoost)
	m.BoostingPct = pct(a.boosting)
	m.PowerslidePct = pct(a.powerslide)
	return m
}
