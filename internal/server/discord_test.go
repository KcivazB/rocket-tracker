package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"rocket-tracker/internal/i18n"
	"rocket-tracker/internal/store"
)

// webhook records the embeds posted to a fake Discord webhook.
type webhook struct {
	mu     sync.Mutex
	posts  []embed
	posted chan struct{}
}

func newWebhook(t *testing.T) (*webhook, string) {
	w := &webhook{posted: make(chan struct{}, 20)}
	ts := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		var body struct {
			Embeds []embed `json:"embeds"`
		}
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &body); err != nil || len(body.Embeds) != 1 {
			t.Errorf("bad webhook body %s", b)
		}
		w.mu.Lock()
		w.posts = append(w.posts, body.Embeds...)
		w.mu.Unlock()
		rw.WriteHeader(http.StatusNoContent)
		w.posted <- struct{}{}
	}))
	t.Cleanup(ts.Close)
	return w, ts.URL
}

func (w *webhook) wait(t *testing.T) embed {
	t.Helper()
	select {
	case <-w.posted:
	case <-time.After(5 * time.Second):
		t.Fatal("no Discord post")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.posts[len(w.posts)-1]
}

func TestDiscordHighlights(t *testing.T) {
	d := &Discord{Lang: i18n.EN}
	m := &store.Match{Result: "win", Overtime: true, Me: store.MeStats{Goals: 3}, TeamScore: 4, OppScore: 3,
		Goals: []store.Goal{{Team: "them"}, {Team: "them"}, {Team: "us"}, {Team: "them"}, {Team: "us"}, {Team: "us"}, {Team: "us"}, {Team: "us"}}}
	got := strings.Join(d.highlights(m, []*store.Match{m}), "|")
	for _, want := range []string{"Overtime", "Hat trick: 3", "Comeback from 2 goals"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing in %q", want, got)
		}
	}
	if d.highlights(&store.Match{Result: "loss", Overtime: true}, nil) != nil {
		t.Error("highlights for a loss")
	}

	// 5th win in a row (an abandoned match in between is skipped).
	var ms []*store.Match
	t0 := time.Date(2026, 10, 1, 20, 0, 0, 0, time.UTC)
	for i, r := range []string{"loss", "win", "win", "abandoned", "win", "win", "win"} {
		ms = append(ms, &store.Match{Online: true, Result: r, StartedAt: t0.Add(time.Duration(i) * 10 * time.Minute)})
	}
	if got := d.highlights(ms[len(ms)-1], ms); len(got) != 1 || !strings.Contains(got[0], "5 wins in a row") {
		t.Errorf("streak: %q", got)
	}
}

func TestDiscordSessionRecap(t *testing.T) {
	d := &Discord{Lang: i18n.EN, PublicURL: "https://rl.example.com"}
	u := &store.User{Handle: "alice", Name: "Alice"}
	t0 := time.Date(2026, 10, 1, 20, 0, 0, 0, time.UTC)
	mk := func(start time.Time, r string, us, them, goals int, mvp bool) *store.Match {
		return &store.Match{Online: true, Result: r, StartedAt: start, EndedAt: start.Add(6 * time.Minute),
			TeamScore: us, OppScore: them, Me: store.MeStats{Goals: goals}, MVP: mvp}
	}
	ms := []*store.Match{
		mk(t0.Add(-5*time.Hour), "win", 5, 0, 5, true), // an earlier session
		mk(t0, "win", 3, 1, 2, true),
		mk(t0.Add(7*time.Minute), "loss", 0, 2, 0, false),
		mk(t0.Add(14*time.Minute), "win", 2, 1, 1, false),
	}
	e, ok := d.sessionRecap(u, ms, ms[3].EndedAt)
	if !ok || e.Title != "Alice's session is over" || e.Description != "3 matches · 2W-1L (67%) · diff +1 · 3 goals · 1 MVP" {
		t.Fatalf("%v %+v", ok, e)
	}
	if e.URL != "https://rl.example.com/#/player/alice" {
		t.Fatalf("url %q", e.URL)
	}
	if _, ok := d.sessionRecap(u, ms[:3], ms[2].EndedAt); ok {
		t.Fatal("recap for 2 matches")
	}
}

func TestLastWeeklySlot(t *testing.T) {
	loc := time.FixedZone("CEST", 2*3600)
	for _, c := range []struct{ now, want string }{
		{"2026-10-05 09:00", "2026-10-05 09:00"}, // Monday, on time
		{"2026-10-05 08:59", "2026-09-28 09:00"}, // Monday, too early: last week's
		{"2026-10-09 14:00", "2026-10-05 09:00"}, // Friday
		{"2026-10-11 23:00", "2026-10-05 09:00"}, // Sunday
	} {
		now, _ := time.ParseInLocation("2006-01-02 15:04", c.now, loc)
		if got := lastWeeklySlot(now).Format("2006-01-02 15:04"); got != c.want {
			t.Errorf("%s: %s, want %s", c.now, got, c.want)
		}
	}
}

func TestDiscordFromAgentAndWeekly(t *testing.T) {
	hook, hookURL := newWebhook(t)
	e := newHub(t, nil)
	e.s.Hub.Discord = &Discord{WebhookURL: hookURL, PublicURL: e.ts.URL, Lang: i18n.EN}
	ctx := context.Background()
	alice, bob := e.devLogin(t, "Alice"), e.devLogin(t, "Bob")
	base := e.ts.URL

	// Bob opts out.
	if code, body := call(t, bob, "PUT", base+"/api/config", `{"discord_off":true}`); code != 200 || !strings.Contains(body, `"discord_off":true`) {
		t.Fatalf("opt out %d %s", code, body)
	}
	token := func(c *http.Client) []string {
		_, body := call(t, c, "POST", base+"/api/devices", `{"name":"PC"}`)
		var created struct {
			Token string `json:"token"`
		}
		json.Unmarshal([]byte(body), &created)
		return []string{"Authorization", "Bearer " + created.Token, "Origin", ""}
	}
	send := func(auth []string, i int, result string, ot bool) {
		start := time.Now().Add(time.Duration(i*7-30) * time.Minute).UTC()
		m := fmt.Sprintf(`{"key":"k%d","match":{"guid":"G%d","online":true,"started_at":%q,"ended_at":%q,"mode":"2v2","result":%q,"overtime":%v,"team_score":2,"opp_score":1}}`,
			i, i, start.Format(time.RFC3339), start.Add(6*time.Minute).Format(time.RFC3339), result, ot)
		if code, body := call(t, &http.Client{}, "POST", base+"/api/agent/matches", m, auth...); code != 200 {
			t.Fatalf("upload %d %s", code, body)
		}
	}
	aliceAuth, bobAuth := token(alice), token(bob)
	send(bobAuth, 0, "win", true) // opted out: nothing
	send(aliceAuth, 1, "win", true)
	if p := hook.wait(t); p.Title != "Alice wins 2-1 in 2v2" || !strings.Contains(p.Description, "Overtime") {
		t.Fatalf("%+v", p)
	}
	send(aliceAuth, 2, "loss", false)
	send(aliceAuth, 3, "win", false)

	// 30 minutes later, the session recap.
	d := e.s.Hub.Discord
	d.closeSessions(ctx, e.st, time.Now().Add(time.Minute))
	select {
	case <-hook.posted:
		t.Fatal("session closed too early")
	case <-time.After(100 * time.Millisecond):
	}
	d.closeSessions(ctx, e.st, time.Now().Add(time.Hour))
	if p := hook.wait(t); p.Title != "Alice's session is over" || !strings.HasPrefix(p.Description, "3 matches · 2W-1L") {
		t.Fatalf("%+v", p)
	}

	// Weekly leaderboard: 5 decided matches needed, opted-out players left out.
	ua, _ := e.st.UserByHandle(ctx, "alice")
	ub, _ := e.st.UserByHandle(ctx, "bob")
	for i := 0; i < 6; i++ {
		start := time.Now().Add(-time.Duration(48+i) * time.Hour).UTC()
		for _, u := range []*store.User{ua, ub} {
			m := &store.Match{GUID: fmt.Sprintf("W%d-%d", u.ID, i), Online: true, Mode: "2v2", Result: "loss",
				StartedAt: start, EndedAt: start.Add(6 * time.Minute), TeamScore: 1, OppScore: 2}
			if err := e.st.User(u.ID).Save(ctx, m); err != nil {
				t.Fatal(err)
			}
		}
	}
	slot := lastWeeklySlot(time.Now().AddDate(0, 0, 7))
	d.weekly = slot.AddDate(0, 0, -7) // last recap a week ago: this one is due
	d.weeklyRecap(ctx, e.st, slot.Add(time.Minute))
	p := hook.wait(t)
	if p.Title != "🏆 Leaderboard of the week" || !strings.Contains(p.Description, "🥇 **Alice** — 22% (2W-7L)") || strings.Contains(p.Description, "Bob") {
		t.Fatalf("%+v", p)
	}
	d.weeklyRecap(ctx, e.st, slot.Add(2*time.Minute))
	select {
	case <-hook.posted:
		t.Fatal("weekly recap posted twice")
	case <-time.After(100 * time.Millisecond):
	}
}
