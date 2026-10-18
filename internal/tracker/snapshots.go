package tracker

import "rocket-tracker/internal/domain"

func cloneValues[T any](in []T) []T {
	if in == nil {
		return nil
	}
	out := make([]T, len(in))
	copy(out, in)
	return out
}

func cloneTraining(in *domain.Training) *domain.Training {
	v := *in
	v.ActiveIntervals = cloneValues(in.ActiveIntervals)
	return &v
}

// Result ownership ends only after commit. Copy nested telemetry too: store
// normalization and user callbacks must never change a pending attempt.
func cloneResult(in *domain.Match) *domain.Match {
	v := *in
	v.Players = cloneValues(in.Players)
	v.Goals = cloneValues(in.Goals)
	for i := range v.Goals {
		if p := v.Goals[i].Impact; p != nil {
			x := *p
			v.Goals[i].Impact = &x
		}
	}
	v.Events = cloneValues(in.Events)
	v.Demolitions = cloneValues(in.Demolitions)
	if in.Statfeed != nil {
		v.Statfeed = make(map[string]int, len(in.Statfeed))
		for k, n := range in.Statfeed {
			v.Statfeed[k] = n
		}
	}
	if in.Movement != nil {
		x := *in.Movement
		if x.CarSampleS != nil {
			n := *x.CarSampleS
			x.CarSampleS = &n
		}
		v.Movement = &x
	}
	if in.Hits != nil {
		x := *in.Hits
		x.Points = cloneValues(in.Hits.Points)
		v.Hits = &x
	}
	return &v
}
