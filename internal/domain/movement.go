package domain

import (
	"encoding/json"
	"fmt"
)

// UnmarshalJSON checks availability before JSON nulls or absent fields become
// numeric zeros. Only positive car time claims a complete set of car aggregates;
// legacy records and demolition-only observations retain their tolerant shape.
func (m *Movement) UnmarshalJSON(data []byte) error {
	type plain Movement
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if decoded.CarSampleS != nil && *decoded.CarSampleS > 0 {
		var present struct {
			AvgSpeed      *float64 `json:"avg_speed"`
			SupersonicPct *float64 `json:"supersonic_pct"`
			GroundPct     *float64 `json:"ground_pct"`
			WallPct       *float64 `json:"wall_pct"`
			AirPct        *float64 `json:"air_pct"`
			AvgBoost      *float64 `json:"avg_boost"`
			ZeroBoostPct  *float64 `json:"zero_boost_pct"`
			FullBoostPct  *float64 `json:"full_boost_pct"`
			BoostingPct   *float64 `json:"boosting_pct"`
			PowerslidePct *float64 `json:"powerslide_pct"`
		}
		if err := json.Unmarshal(data, &present); err != nil {
			return err
		}
		for _, field := range []struct {
			name  string
			value *float64
		}{
			{"avg_speed", present.AvgSpeed}, {"supersonic_pct", present.SupersonicPct},
			{"ground_pct", present.GroundPct}, {"wall_pct", present.WallPct}, {"air_pct", present.AirPct},
			{"avg_boost", present.AvgBoost}, {"zero_boost_pct", present.ZeroBoostPct},
			{"full_boost_pct", present.FullBoostPct}, {"boosting_pct", present.BoostingPct},
			{"powerslide_pct", present.PowerslidePct},
		} {
			if field.value == nil {
				return fmt.Errorf("movement: %s must be a number when car_sample_s is positive", field.name)
			}
		}
	}
	*m = Movement(decoded)
	return nil
}

// MarshalJSON preserves legacy numeric fields, but never publishes invented
// car averages when only demolition time was observed.
func (m Movement) MarshalJSON() ([]byte, error) {
	type plain Movement
	if m.CarSampleS == nil || *m.CarSampleS > 0 {
		return json.Marshal(plain(m))
	}
	return json.Marshal(struct {
		plain
		AvgSpeed      *float64 `json:"avg_speed"`
		SupersonicPct *float64 `json:"supersonic_pct"`
		GroundPct     *float64 `json:"ground_pct"`
		WallPct       *float64 `json:"wall_pct"`
		AirPct        *float64 `json:"air_pct"`
		AvgBoost      *float64 `json:"avg_boost"`
		ZeroBoostPct  *float64 `json:"zero_boost_pct"`
		FullBoostPct  *float64 `json:"full_boost_pct"`
		BoostingPct   *float64 `json:"boosting_pct"`
		PowerslidePct *float64 `json:"powerslide_pct"`
	}{plain: plain(m)})
}
