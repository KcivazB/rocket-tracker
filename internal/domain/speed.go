package domain

// SpeedUnit declares the source unit; magnitude is never evidence of a unit.
// An omitted field in historical persisted records retains its legacy uu/s
// interpretation. New decoder observations use unknown unless configured.
type SpeedUnit string

const (
	SpeedUnknown           SpeedUnit = "unknown"
	SpeedUUPerSecond       SpeedUnit = "uu/s"
	SpeedKilometresPerHour SpeedUnit = "km/h"
)

func (u SpeedUnit) Valid() bool {
	return u == "" || u == SpeedUnknown || u == SpeedUUPerSecond || u == SpeedKilometresPerHour
}
