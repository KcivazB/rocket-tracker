package application

import (
	"fmt"
	"math"

	"rocket-tracker/internal/domain"
)

// ValidateMatchQuantities is shared by live uploads and both import formats.
// Coordinates and derived signed values (such as goal difference) are not quantities.
func ValidateMatchQuantities(m *domain.Match) error {
	if !m.SpeedUnit.Valid() {
		return fmt.Errorf("%w: unknown speed unit %q", ErrInvalidMatch, m.SpeedUnit)
	}
	if err := nonnegative("match", m.DurationS, m.OvertimeS, float64(m.TeamSize), float64(m.TeamScore), float64(m.OppScore), float64(m.RegulationS)); err != nil {
		return err
	}
	me := m.Me
	if err := nonnegative("me", float64(me.Score), float64(me.Goals), float64(me.Shots), float64(me.Assists), float64(me.Saves), float64(me.Touches), float64(me.Demos)); err != nil {
		return err
	}
	for _, p := range m.Players {
		if err := nonnegative("players", float64(p.Score), float64(p.Goals), float64(p.Shots), float64(p.Assists), float64(p.Saves), float64(p.Touches), float64(p.Demos)); err != nil {
			return err
		}
	}
	if v := m.Movement; v != nil {
		if v.CarSampleS != nil {
			if err := nonnegative("movement car sample", *v.CarSampleS); err != nil {
				return err
			}
		}
		if err := nonnegative("movement", v.SampleS, v.AvgSpeed, v.SupersonicPct, v.GroundPct, v.WallPct, v.AirPct, v.AvgBoost, v.ZeroBoostPct, v.FullBoostPct, v.BoostingPct, v.PowerslidePct, v.DemolishedS); err != nil {
			return err
		}
	}
	if h := m.Hits; h != nil {
		if err := nonnegative("hits", float64(h.Count), h.AvgSpeed, h.MaxSpeed); err != nil {
			return err
		}
		for _, p := range h.Points {
			if err := nonnegative("hit speeds", p.Pre, p.Post); err != nil {
				return err
			}
		}
	}
	for _, g := range m.Goals {
		if err := nonnegative("goal speed", g.Speed); err != nil {
			return err
		}
	}
	for _, e := range m.Events {
		if err := nonnegative("event time", e.T); err != nil {
			return err
		}
	}
	for _, count := range m.Statfeed {
		if err := nonnegative("statfeed", float64(count)); err != nil {
			return err
		}
	}
	return nil
}

func nonnegative(field string, values ...float64) error {
	for _, v := range values {
		if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("%w: %s quantities must be finite and nonnegative", ErrInvalidMatch, field)
		}
	}
	return nil
}
