package domain

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

// TrainingInterval is one measured block of activity inside a training.
type TrainingInterval struct {
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
}

// Training is detected game practice or a dashboard warm-up timer.
type Training struct {
	// Nil retains historical full-span activity; a non-nil empty slice is zero.
	ActiveIntervals []TrainingInterval `json:"active_intervals,omitzero"`
	SpeedUnit       SpeedUnit          `json:"speed_unit,omitempty"`
	ID              int64              `json:"id"`
	StartedAt       time.Time          `json:"started_at"`
	EndedAt         time.Time          `json:"ended_at"`
	Source          string             `json:"source"`  // "game" or "timer"
	Focus           string             `json:"focus"`   // the mechanic worked on (see Focuses); "" = not said
	Profile         string             `json:"profile"` // the account (ProfileKey of a match); "" = not known
	Arena           string             `json:"arena"`
	// Touches and their ball speeds (SpeedUnit), when detected in the game.
	Touches     int     `json:"touches"`
	AvgHitSpeed float64 `json:"avg_hit_speed"`
	MaxHitSpeed float64 `json:"max_hit_speed"`
}

// Focuses are the mechanics a training can be about.
var Focuses = []string{"freeplay", "kickoff", "shooting", "saves", "aerials", "power", "boost", "dribble", "recovery", "rotation"}

// Training rules.
const (
	MaxTraining          = 12 * time.Hour
	MaxTrainingIntervals = 4096
	// TrainingJoin: two trainings this close are one (the game's and the
	// timer's record of the same warm-up, or a pack after freeplay).
	TrainingJoin = 2 * time.Minute
)

// ErrInvalidTraining wraps the reasons a training is refused. Its text keeps
// the historical store error contract.
var ErrInvalidTraining = errors.New("store: invalid training")

// ValidFocus reports whether f can be a training's focus.
func ValidFocus(f string) bool {
	return f == "" || slices.Contains(Focuses, f)
}

// Normalize cleans the fields and checks them.
func (t *Training) Normalize() error {
	outerStart, outerEnd := t.StartedAt, t.EndedAt
	t.StartedAt = t.StartedAt.UTC().Truncate(time.Second)
	t.EndedAt = t.EndedAt.UTC().Truncate(time.Second)
	t.Focus = strings.ToLower(strings.TrimSpace(t.Focus))
	t.Profile = strings.ToLower(strings.TrimSpace(t.Profile))
	switch {
	case outerStart.IsZero() || outerEnd.Before(outerStart) || outerStart.Year() < 1 || outerEnd.Year() > 9999:
		return fmt.Errorf("%w: invalid times", ErrInvalidTraining)
	case outerEnd.Sub(outerStart) > MaxTraining:
		return fmt.Errorf("%w: longer than %v", ErrInvalidTraining, MaxTraining)
	case t.Source != "game" && t.Source != "timer":
		return fmt.Errorf("%w: source must be game or timer", ErrInvalidTraining)
	case !ValidFocus(t.Focus):
		return fmt.Errorf("%w: unknown focus %q", ErrInvalidTraining, t.Focus)
	case len(t.Profile) > 200 || len(t.Arena) > 200:
		return fmt.Errorf("%w: field too long", ErrInvalidTraining)
	case !t.SpeedUnit.Valid():
		return fmt.Errorf("%w: unknown speed unit %q", ErrInvalidTraining, t.SpeedUnit)
	case math.IsNaN(t.AvgHitSpeed) || math.IsInf(t.AvgHitSpeed, 0) || math.IsNaN(t.MaxHitSpeed) || math.IsInf(t.MaxHitSpeed, 0):
		return fmt.Errorf("%w: non-finite speed", ErrInvalidTraining)
	case t.Touches < 0 || t.AvgHitSpeed < 0 || t.MaxHitSpeed < 0:
		return fmt.Errorf("%w: negative touches or speed", ErrInvalidTraining)
	}
	if len(t.ActiveIntervals) > MaxTrainingIntervals {
		return fmt.Errorf("%w: too many active intervals", ErrInvalidTraining)
	}
	if t.ActiveIntervals != nil {
		intervals := slices.Clone(t.ActiveIntervals)
		for i := range intervals {
			v := &intervals[i]
			if v.StartedAt.IsZero() || v.EndedAt.Before(v.StartedAt) || v.StartedAt.Before(outerStart) || v.EndedAt.After(outerEnd) {
				return fmt.Errorf("%w: invalid active interval", ErrInvalidTraining)
			}
			v.StartedAt = v.StartedAt.UTC().Truncate(time.Second)
			v.EndedAt = v.EndedAt.UTC().Truncate(time.Second)
		}
		t.ActiveIntervals = unionIntervals(intervals)
	}
	return nil
}

// ActiveDuration uses chronology only for historical records without intervals.
func (t Training) ActiveDuration() time.Duration {
	if t.ActiveIntervals == nil {
		return t.EndedAt.Sub(t.StartedAt)
	}
	var d time.Duration
	for _, v := range unionIntervals(slices.Clone(t.ActiveIntervals)) {
		d += v.EndedAt.Sub(v.StartedAt)
	}
	return d
}

func (t Training) activity() []TrainingInterval {
	if t.ActiveIntervals == nil {
		return []TrainingInterval{{t.StartedAt, t.EndedAt}}
	}
	return slices.Clone(t.ActiveIntervals)
}

func unionIntervals(in []TrainingInterval) []TrainingInterval {
	slices.SortFunc(in, func(a, b TrainingInterval) int { return a.StartedAt.Compare(b.StartedAt) })
	out := make([]TrainingInterval, 0, len(in))
	for _, v := range in {
		if !v.EndedAt.After(v.StartedAt) {
			continue
		}
		if len(out) > 0 && !v.StartedAt.After(out[len(out)-1].EndedAt) {
			if v.EndedAt.After(out[len(out)-1].EndedAt) {
				out[len(out)-1].EndedAt = v.EndedAt
			}
		} else {
			out = append(out, v)
		}
	}
	return out
}

// CanMerge guards the outer bound before changing either record.
func (t Training) CanMerge(o *Training) bool {
	return maxTime(t.EndedAt, o.EndedAt).Sub(minTime(t.StartedAt, o.StartedAt)) <= MaxTraining && len(unionIntervals(append(t.activity(), o.activity()...))) <= MaxTrainingIntervals
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// Merge folds o (the same warm-up, recorded again or by the other source)
// into t. Inputs must have passed Normalize. An oversized union leaves t
// unchanged; persistence checks CanMerge and keeps a separate record instead.
func (t *Training) Merge(o *Training) {
	if !t.CanMerge(o) {
		return
	}
	historical := t.ActiveIntervals == nil && o.ActiveIntervals == nil
	t.ActiveIntervals = unionIntervals(append(t.activity(), o.activity()...))
	if historical && len(t.ActiveIntervals) == 1 && t.ActiveIntervals[0].StartedAt.Equal(minTime(t.StartedAt, o.StartedAt)) && t.ActiveIntervals[0].EndedAt.Equal(maxTime(t.EndedAt, o.EndedAt)) {
		t.ActiveIntervals = nil
	}
	if o.StartedAt.Before(t.StartedAt) {
		t.StartedAt = o.StartedAt
	}
	if o.EndedAt.After(t.EndedAt) {
		t.EndedAt = o.EndedAt
	}
	if o.Source == "game" {
		t.Source = "game"
	}
	if t.Focus == "" {
		t.Focus = o.Focus
	}
	if t.Profile == "" {
		t.Profile = o.Profile
	}
	if t.Arena == "" {
		t.Arena = o.Arena
	}
	// Timer records without touches carry no speed evidence. Otherwise keep
	// existing metrics when units differ rather than compare incompatible values.
	sameUnit := t.SpeedUnit == o.SpeedUnit ||
		(t.SpeedUnit == "" && o.SpeedUnit == SpeedUUPerSecond) ||
		(t.SpeedUnit == SpeedUUPerSecond && o.SpeedUnit == "")
	if t.Touches == 0 && t.AvgHitSpeed == 0 && t.MaxHitSpeed == 0 {
		t.SpeedUnit = o.SpeedUnit
		sameUnit = true
	}
	if o.Touches > t.Touches {
		t.Touches = o.Touches
		if sameUnit {
			t.AvgHitSpeed = o.AvgHitSpeed
		}
	}
	if sameUnit {
		t.MaxHitSpeed = max(t.MaxHitSpeed, o.MaxHitSpeed)
	}
}
