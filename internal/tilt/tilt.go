// Package tilt decides, right after a match, whether to suggest a break: a
// losing streak, or a session longer than the point where the player's
// results usually drop. The numbers come from the player's own history.
package tilt

import (
	"sort"
	"time"

	"rocket-tracker/internal/store"
)

const (
	// SessionGap separates two sessions (same rule as the dashboard).
	SessionGap = 30 * time.Minute
	// Fresh: older matches (an agent's queue sent late) raise no alert.
	Fresh = 20 * time.Minute
	// MinSample is the history needed before quoting a win rate.
	MinSample = 5
	// Long-session rule: from this many matches, when the win rate from that
	// match on is at least LongDrop below the average over LongSample games.
	LongFrom   = 6
	LongSample = 10
	LongDrop   = 0.08
)

// Alert describes why a break is suggested. The client words it.
type Alert struct {
	Kind     string  `json:"kind"`     // "streak" or "long_session"
	Losses   int     `json:"losses"`   // streak: consecutive losses
	Matches  int     `json:"matches"`  // matches played in the session
	WinRate  float64 `json:"win_rate"` // win rate of the next match in this situation (0..1), when Sample >= MinSample
	Sample   int     `json:"sample"`   // past situations WinRate is computed from
	Baseline float64 `json:"baseline"` // overall win rate (0..1)
}

type game struct {
	win        bool
	start, end time.Time
}

// Check looks at the player's matches (in any order, the newest included)
// after a match ended. streak is the number of consecutive losses that
// raises an alert; 0 disables alerts.
func Check(ms []*store.Match, streak int, now time.Time) *Alert {
	if streak <= 0 {
		return nil
	}
	var gs []game
	for _, m := range ms {
		if m.Result != "win" && m.Result != "loss" {
			continue // abandoned: neither
		}
		gs = append(gs, game{win: m.Result == "win", start: m.StartedAt, end: end(m)})
	}
	if len(gs) == 0 {
		return nil
	}
	sort.Slice(gs, func(i, j int) bool { return gs[i].start.Before(gs[j].start) })
	last := gs[len(gs)-1]
	if now.Sub(last.end) > Fresh {
		return nil
	}

	// idx[i] is the 1-based position of game i in its session.
	idx := make([]int, len(gs))
	wins := 0
	for i, g := range gs {
		idx[i] = 1
		if i > 0 && g.start.Sub(gs[i-1].end) <= SessionGap {
			idx[i] = idx[i-1] + 1
		}
		if g.win {
			wins++
		}
	}
	baseline := float64(wins) / float64(len(gs))
	n := len(gs)
	session := idx[n-1]

	losses := 0
	for i := n - 1; i >= 0 && !gs[i].win && losses < session; i-- {
		losses++
	}
	if losses >= streak {
		a := &Alert{Kind: "streak", Losses: losses, Matches: session, Baseline: baseline}
		// Past games that followed at least `losses` losses in the same session.
		w := 0
		for i := losses; i < n; i++ {
			if idx[i] <= losses {
				continue
			}
			after := true
			for j := i - losses; j < i; j++ {
				if gs[j].win {
					after = false
					break
				}
			}
			if after {
				a.Sample++
				if gs[i].win {
					w++
				}
			}
		}
		if a.Sample > 0 {
			a.WinRate = float64(w) / float64(a.Sample)
		}
		return a
	}

	// Long session: alert once, at the first match (from LongFrom) where the
	// drop shows.
	alerted := session-1 >= LongFrom && longDrop(gs, idx, session-1, baseline)
	if session >= LongFrom && longDrop(gs, idx, session, baseline) && !alerted {
		a := &Alert{Kind: "long_session", Matches: session, Baseline: baseline}
		w := 0
		for i := range gs {
			if idx[i] > session { // the matches that came after this many
				a.Sample++
				if gs[i].win {
					w++
				}
			}
		}
		if a.Sample > 0 {
			a.WinRate = float64(w) / float64(a.Sample)
		}
		return a
	}
	return nil
}

// longDrop: the games played after the k-th of a session are clearly below
// the average.
func longDrop(gs []game, idx []int, k int, baseline float64) bool {
	n, w := 0, 0
	for i := range gs {
		if idx[i] > k {
			n++
			if gs[i].win {
				w++
			}
		}
	}
	return n >= LongSample && float64(w)/float64(n) <= baseline-LongDrop
}

func end(m *store.Match) time.Time {
	if !m.EndedAt.IsZero() {
		return m.EndedAt
	}
	return m.StartedAt.Add(time.Duration(m.DurationS * float64(time.Second)))
}
