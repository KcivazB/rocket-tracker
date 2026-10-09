package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"rocket-tracker/internal/i18n"
	"rocket-tracker/internal/store"
)

// webhook is a fake Discord: the client it returns sends every request to it,
// whatever the (real-looking) webhook URL, and it records which webhook got
// which embed.
type webhook struct {
	mu     sync.Mutex
	posts  []post
	seen   int // posts already returned by wait
	posted chan struct{}
}

type post struct {
	hook string // webhook path, e.g. /api/webhooks/1/alice
	embed
}

func newWebhook(t *testing.T) (*webhook, *http.Client) {
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
		w.posts = append(w.posts, post{r.URL.Path, body.Embeds[0]})
		w.mu.Unlock()
		rw.WriteHeader(http.StatusNoContent)
		w.posted <- struct{}{}
	}))
	t.Cleanup(ts.Close)
	target, _ := url.Parse(ts.URL)
	return w, &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
		return http.DefaultTransport.RoundTrip(r)
	})}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func (w *webhook) wait(t *testing.T) post {
	t.Helper()
	select {
	case <-w.posted:
	case <-time.After(5 * time.Second):
		t.Fatal("no Discord post")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seen++
	return w.posts[w.seen-1]
}

func (w *webhook) none(t *testing.T) {
	t.Helper()
	select {
	case <-w.posted:
		w.mu.Lock()
		defer w.mu.Unlock()
		t.Fatalf("unexpected post %+v", w.posts[len(w.posts)-1])
	case <-time.After(150 * time.Millisecond):
	}
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

const (
	aliceHook = "https://discord.com/api/webhooks/1/alice"
	bobHook   = "https://discord.com/api/webhooks/2/bob"
)

func TestDiscordPerPlayer(t *testing.T) {
	hook, client := newWebhook(t)
	e := newHub(t, nil)
	e.s.Hub.Discord = &Discord{PublicURL: e.ts.URL, Lang: i18n.EN, Client: client}
	ctx := context.Background()
	alice, bob, carol := e.devLogin(t, "Alice"), e.devLogin(t, "Bob"), e.devLogin(t, "Carol")
	base := e.ts.URL

	// Only Discord webhook URLs are accepted (the server must not call anything else).
	if code, _ := call(t, alice, "PUT", base+"/api/config", `{"discord_webhook":"http://192.168.1.10/hook"}`); code != 400 {
		t.Fatalf("non-Discord URL accepted: %d", code)
	}
	if code, _ := call(t, alice, "POST", base+"/api/discord/test", `{"webhook":"https://evil.example/api/webhooks/1/x"}`); code != 400 {
		t.Fatalf("test of a non-Discord URL: %d", code)
	}
	for _, x := range []struct {
		c    *http.Client
		hook string
	}{{alice, aliceHook}, {bob, bobHook}} {
		if code, body := call(t, x.c, "PUT", base+"/api/config", `{"discord_webhook":"`+x.hook+`"}`); code != 200 || !strings.Contains(body, x.hook) {
			t.Fatalf("set webhook %d %s", code, body)
		}
	}
	// The test button posts to the webhook typed in the form.
	if code, body := call(t, alice, "POST", base+"/api/discord/test", `{"webhook":"`+aliceHook+`"}`); code != 200 {
		t.Fatalf("test %d %s", code, body)
	}
	if p := hook.wait(t); p.hook != "/api/webhooks/1/alice" || p.Title != "Rocket Tracker is connected" {
		t.Fatalf("%+v", p)
	}
	// Another player never sees the webhook.
	if _, body := call(t, bob, "GET", base+"/api/players/alice/config", ""); strings.Contains(body, "webhooks") {
		t.Fatalf("webhook leaked: %s", body)
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
	aliceAuth, carolAuth := token(alice), token(carol)
	send(carolAuth, 0, "win", true) // no webhook: nothing
	hook.none(t)
	send(aliceAuth, 1, "win", true)
	if p := hook.wait(t); p.hook != "/api/webhooks/1/alice" || p.Title != "Alice wins 2-1 in 2v2" || !strings.Contains(p.Description, "Overtime") {
		t.Fatalf("%+v", p)
	}
	send(aliceAuth, 2, "loss", false)
	send(aliceAuth, 3, "win", false)

	// 30 minutes later, the session recap, on Alice's webhook only.
	d := e.s.Hub.Discord
	d.closeSessions(ctx, e.st, time.Now().Add(time.Minute))
	hook.none(t)
	d.closeSessions(ctx, e.st, time.Now().Add(time.Hour))
	if p := hook.wait(t); p.hook != "/api/webhooks/1/alice" || p.Title != "Alice's session is over" || !strings.HasPrefix(p.Description, "3 matches · 2W-1L") {
		t.Fatalf("%+v", p)
	}

	// Weekly leaderboard: to every webhook, ranking the players who set one
	// (5 decided matches at least): Carol has none, she stays out.
	for _, h := range []string{"alice", "bob", "carol"} {
		u, _ := e.st.UserByHandle(ctx, h)
		for i := 0; i < 6; i++ {
			start := time.Now().Add(-time.Duration(48+i) * time.Hour).UTC()
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
	got := map[string]string{}
	for i := 0; i < 2; i++ {
		p := hook.wait(t)
		got[p.hook] = p.Description
	}
	hook.none(t)
	for _, h := range []string{"/api/webhooks/1/alice", "/api/webhooks/2/bob"} {
		if desc := got[h]; !strings.Contains(desc, "**Alice** — 22% (2W-7L)") || !strings.Contains(desc, "**Bob** — 0% (0W-6L)") || strings.Contains(desc, "Carol") {
			t.Fatalf("%s: %q", h, desc)
		}
	}
	d.weeklyRecap(ctx, e.st, slot.Add(2*time.Minute))
	hook.none(t)
}
