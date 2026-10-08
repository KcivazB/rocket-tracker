package tracker

import (
	"rocket-tracker/internal/statsapi"
	"rocket-tracker/internal/store"
)

// movementAcc integrates spectator car-state samples over real time.
type movementAcc struct {
	total      float64 // all sampled seconds
	car        float64 // seconds with a live car (base for percentages)
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

func bv(b *statsapi.Bool) bool { return b != nil && bool(*b) }

func (a *movementAcc) add(p *statsapi.Player, dt float64) {
	if dt <= 0 {
		return
	}
	a.total += dt
	if bv(p.Demolished) {
		a.demolished += dt
		return
	}
	if p.HasCar != nil && !bool(*p.HasCar) {
		return
	}
	a.car += dt
	if p.Speed != nil {
		a.speed += float64(*p.Speed) * dt
	}
	if bv(p.Supersonic) {
		a.supersonic += dt
	}
	switch {
	case bv(p.OnGround):
		a.ground += dt
	case bv(p.OnWall):
		a.wall += dt
	default:
		a.air += dt
	}
	if p.Boost != nil {
		b := float64(*p.Boost)
		a.boost += b * dt
		if b <= 0.5 {
			a.zeroBoost += dt
		}
		if b >= 99.5 {
			a.fullBoost += dt
		}
	}
	if bv(p.Boosting) {
		a.boosting += dt
	}
	if bv(p.Powersliding) {
		a.powerslide += dt
	}
}

func (a *movementAcc) result() *store.Movement {
	if a.total <= 0 {
		return nil
	}
	m := &store.Movement{SampleS: round1(a.total), DemolishedS: round1(a.demolished)}
	if a.car > 0 {
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
	}
	return m
}
