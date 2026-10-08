package sim

import (
	"bytes"
	"context"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"rocket-tracker/internal/statsapi"
	"rocket-tracker/internal/store"
	"rocket-tracker/internal/tracker"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func freePort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// TestSimulatorToTracker runs the simulator and a real client+tracker in
// both transports and checks every simulated match is recorded consistently.
func TestSimulatorToTracker(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	for _, mode := range []string{"tcp", "ws"} {
		t.Run(mode, func(t *testing.T) {
			port := freePort(t)
			var mu sync.Mutex
			var saved []*store.Match
			tr := tracker.New(tracker.Options{
				Identity: func() tracker.Identity { return tracker.Identity{} }, // auto-detect via Target
				Save: func(m *store.Match) error {
					mu.Lock()
					defer mu.Unlock()
					saved = append(saved, m)
					return nil
				},
			})
			cl := &statsapi.Client{Port: port, RetryInterval: 100 * time.Millisecond, HandshakeTimeout: 300 * time.Millisecond,
				OnEvent: tr.HandleEvent, OnDisconnect: tr.Disconnected}
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			go cl.Run(ctx)

			out := &syncBuf{}
			const n = 8
			if err := Run(ctx, Options{Port: port, Mode: mode, Matches: n, Speed: 150, Seed: 1234, Out: out,
				Kinds: []string{"normal", "forfeit", "abandoned", "offline", "normal", "forfeit", "normal", "offline"}}); err != nil {
				t.Fatal(err)
			}
			time.Sleep(300 * time.Millisecond)
			mu.Lock()
			defer mu.Unlock()
			var summaries []string
			for _, l := range strings.Split(out.String(), "\n") {
				if i := strings.Index(l, " done: "); i >= 0 {
					summaries = append(summaries, l[i+7:])
				}
			}
			if len(summaries) != n {
				t.Fatalf("sim output:\n%s", out.String())
			}
			// Note: the tracker auto-detects identity only for saved
			// matches; every simulated match has >= 30 s of play.
			if len(saved) != n {
				t.Fatalf("saved %d matches, want %d\n%s", len(saved), n, out.String())
			}
			t.Logf("summaries: %q", summaries)
			for i, s := range summaries {
				m := saved[i]
				if m.Me.Name != "SimPlayer" {
					t.Errorf("match %d: me=%q", i, m.Me.Name)
				}
				switch {
				case strings.HasPrefix(s, "abandoned"):
					if m.Result != "abandoned" {
						t.Errorf("match %d: %s => result %s", i, s, m.Result)
					}
					continue
				case strings.Contains(s, " win "):
					if m.Result != "win" {
						t.Errorf("match %d: %s => result %s", i, s, m.Result)
					}
				default:
					if m.Result != "loss" {
						t.Errorf("match %d: %s => result %s", i, s, m.Result)
					}
				}
				if strings.Contains(s, "forfeit") != m.Forfeit {
					t.Errorf("match %d: %s => forfeit %v", i, s, m.Forfeit)
				}
				if strings.Contains(s, "overtime") != m.Overtime {
					t.Errorf("match %d: %s => overtime %v", i, s, m.Overtime)
				}
				if strings.HasPrefix(s, "offline") == m.Online {
					t.Errorf("match %d: %s => online %v", i, s, m.Online)
				}
				if !strings.Contains(s, " "+m.Mode+" ") {
					t.Errorf("match %d: %s => mode %s", i, s, m.Mode)
				}
				blue, orange := m.TeamScore, m.OppScore
				if m.MyTeam == 1 {
					blue, orange = orange, blue
				}
				if !strings.Contains(s, "blue "+itoa(blue)+" - "+itoa(orange)+" orange") {
					t.Errorf("match %d: %s => score blue %d orange %d", i, s, blue, orange)
				}
				if m.Movement == nil || m.Hits == nil || len(m.Goals) != m.TeamScore+m.OppScore {
					t.Errorf("match %d: movement=%v hits=%v goals=%d", i, m.Movement != nil, m.Hits != nil, len(m.Goals))
				}
			}
		})
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

func TestGenerate(t *testing.T) {
	now := time.Now()
	ms := Generate(SeedOptions{N: 300, Seed: 99, Now: now})
	if len(ms) != 300 {
		t.Fatalf("got %d", len(ms))
	}
	results := map[string]int{}
	for i, m := range ms {
		results[m.Result]++
		if i > 0 && m.StartedAt.Before(ms[i-1].StartedAt) {
			t.Fatal("not sorted")
		}
		if m.StartedAt.Before(now.Add(-91*24*time.Hour)) || m.StartedAt.After(now) {
			t.Fatalf("out of range: %v", m.StartedAt)
		}
		// Regression: seeded matches overlapped (next start before the previous end).
		if i > 0 && m.StartedAt.Before(ms[i-1].EndedAt) {
			t.Fatalf("match %d starts %v before previous end %v", i, m.StartedAt, ms[i-1].EndedAt)
		}
		if m.EndedAt.After(now) {
			t.Fatalf("ends in the future: %v", m.EndedAt)
		}
		// Regression: abandoned matches had a score but no goals.
		if len(m.Goals) != m.TeamScore+m.OppScore {
			t.Fatalf("goals %d vs %d-%d", len(m.Goals), m.TeamScore, m.OppScore)
		}
		if (m.Result == "win") != (m.TeamScore > m.OppScore) && m.Result != "abandoned" {
			t.Fatalf("result %s with %d-%d", m.Result, m.TeamScore, m.OppScore)
		}
	}
	if results["win"] < 100 || results["loss"] < 100 {
		t.Fatalf("results %v", results)
	}
}
