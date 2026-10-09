package tilt

import (
	"testing"
	"time"

	"rocket-tracker/internal/store"
)

var t0 = time.Date(2026, 10, 1, 18, 0, 0, 0, time.UTC)

// sessions builds matches from strings of W/L, one string per session (a
// day apart), each match 6 minutes long with 1 minute between them.
func sessions(ss ...string) ([]*store.Match, time.Time) {
	var ms []*store.Match
	var last time.Time
	for d, s := range ss {
		t := t0.Add(time.Duration(d) * 24 * time.Hour)
		for _, c := range s {
			r := "loss"
			if c == 'W' {
				r = "win"
			}
			ms = append(ms, &store.Match{Result: r, StartedAt: t, EndedAt: t.Add(6 * time.Minute)})
			last = t.Add(6 * time.Minute)
			t = t.Add(7 * time.Minute)
		}
	}
	return ms, last
}

func TestStreakWithHistory(t *testing.T) {
	// After 3 losses, the next match was lost 4 times out of 5 in the past.
	ms, now := sessions("LLLL", "LLLL", "LLLW", "LLLL", "LLLL", "WLLL")
	a := Check(ms, 3, now)
	if a == nil || a.Kind != "streak" || a.Losses != 3 {
		t.Fatalf("%+v", a)
	}
	if a.Sample != 5 || a.WinRate != 0.2 {
		t.Fatalf("sample %d win rate %v", a.Sample, a.WinRate)
	}
	if a.Baseline != 2.0/24 {
		t.Fatalf("baseline %v", a.Baseline)
	}
}

func TestStreakBelowThreshold(t *testing.T) {
	ms, now := sessions("WWLL")
	if a := Check(ms, 3, now); a != nil {
		t.Fatalf("%+v", a)
	}
	if a := Check(ms, 2, now); a == nil || a.Losses != 2 || a.Sample != 0 {
		t.Fatalf("%+v", a)
	}
}

func TestStreakStopsAtSessionStart(t *testing.T) {
	// Yesterday's losses do not count in today's streak.
	ms, now := sessions("LLLL", "LL")
	if a := Check(ms, 3, now); a != nil {
		t.Fatalf("%+v", a)
	}
}

func TestDisabledAndStale(t *testing.T) {
	ms, now := sessions("LLLL")
	if a := Check(ms, 0, now); a != nil {
		t.Fatal("alert while disabled")
	}
	if a := Check(ms, 3, now.Add(time.Hour)); a != nil {
		t.Fatal("alert for an old match")
	}
}

func TestAbandonedIgnored(t *testing.T) {
	ms, now := sessions("LLL")
	ms[1].Result = "abandoned"
	if a := Check(ms, 3, now); a != nil {
		t.Fatalf("%+v", a)
	}
}

func TestLongSession(t *testing.T) {
	// Past sessions: good for 6 matches, then mostly losses.
	past := "WWWLWWLLLL"
	ms, _ := sessions(past, past, past, "WLWLWW")
	now := ms[len(ms)-1].EndedAt
	a := Check(ms, 3, now)
	if a == nil || a.Kind != "long_session" || a.Matches != 6 {
		t.Fatalf("%+v", a)
	}
	if a.Sample != 12 || a.WinRate != 0 || a.WinRate >= a.Baseline {
		t.Fatalf("%+v", a)
	}
	// Only once: the 7th match (a win) raises nothing.
	ms = append(ms, &store.Match{Result: "win", StartedAt: now.Add(time.Minute), EndedAt: now.Add(7 * time.Minute)})
	if a := Check(ms, 3, now.Add(7*time.Minute)); a != nil {
		t.Fatalf("second alert %+v", a)
	}
}
