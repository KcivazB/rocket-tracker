package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"rocket-tracker/internal/i18n"
	"rocket-tracker/internal/store"
	"rocket-tracker/internal/tilt"
)

// Discord posts the group's highlights to a Discord channel through a
// webhook: notable matches, a recap when a player's session ends and a weekly
// leaderboard (Monday morning, server time). Players can opt out in their
// settings (discord_off).
type Discord struct {
	WebhookURL string
	PublicURL  string    // links to the dashboard
	Lang       i18n.Lang // language of the posts
	StateFile  string    // remembers the last weekly recap ("" = in memory)
	Client     *http.Client
	Log        *slog.Logger

	mu       sync.Mutex
	sessions map[int64]*openSession // players with a session in progress
	weekly   time.Time              // last weekly recap
}

type openSession struct {
	user *store.User
	last time.Time // end of the last match
}

// WeeklyAt is when the weekly recap is posted: Monday at this hour.
const WeeklyAt = 9

const (
	colorWin  = 0x3987e5
	colorInfo = 0x8a96a8
	colorGold = 0xe5b839
)

type embed struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	URL         string `json:"url,omitempty"`
	Color       int    `json:"color,omitempty"`
}

func (d *Discord) t(key string, args ...any) string { return i18n.Tf(d.Lang, key, args...) }

func (d *Discord) post(ctx context.Context, e embed) error {
	b, _ := json.Marshal(map[string]any{"username": "Rocket Tracker", "embeds": []embed{e}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.WebhookURL, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c := d.Client
	if c == nil {
		c = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("discord webhook: HTTP %d", resp.StatusCode)
	}
	return nil
}

func (d *Discord) send(e embed) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := d.post(ctx, e); err != nil && d.Log != nil {
		d.Log.Warn("discord post failed", "title", e.Title, "err", err)
	}
}

func (d *Discord) playerURL(u *store.User) string {
	return strings.TrimRight(d.PublicURL, "/") + "/#/player/" + u.Handle
}

func displayName(u *store.User) string {
	if u.Name != "" {
		return u.Name
	}
	return u.Handle
}

// Match is called when an agent sends a match: it posts the match's
// highlights, if any, and keeps the session open for its recap. ms is the
// player's whole history, m included.
func (d *Discord) Match(u *store.User, m *store.Match, ms []*store.Match, now time.Time) {
	if !m.Online || now.Sub(m.EndedAt) > tilt.Fresh {
		return // games against bots, or an agent's queue sent late
	}
	d.mu.Lock()
	if d.sessions == nil {
		d.sessions = map[int64]*openSession{}
	}
	if s := d.sessions[u.ID]; s == nil || m.EndedAt.After(s.last) {
		d.sessions[u.ID] = &openSession{user: u, last: m.EndedAt}
	}
	d.mu.Unlock()

	if lines := d.highlights(m, ms); len(lines) > 0 {
		go d.send(embed{
			Title:       d.t("discord.matchTitle", displayName(u), m.TeamScore, m.OppScore, m.Mode),
			Description: strings.Join(lines, "\n"),
			URL:         d.playerURL(u) + fmt.Sprintf("/match/%d", m.ID),
			Color:       colorWin,
		})
	}
}

// highlights are the notable facts of a won match.
func (d *Discord) highlights(m *store.Match, ms []*store.Match) []string {
	if m.Result != "win" {
		return nil
	}
	var out []string
	if m.Overtime {
		out = append(out, d.t("discord.overtime"))
	}
	if m.Me.Goals >= 3 {
		out = append(out, d.t("discord.hatTrick", m.Me.Goals))
	}
	if deficit := maxDeficit(m.Goals); deficit >= 2 {
		out = append(out, d.t("discord.comeback", deficit))
	}
	if n := winStreak(ms); n >= 5 && n%5 == 0 {
		out = append(out, d.t("discord.streak", n))
	}
	return out
}

// maxDeficit is the largest number of goals the player's team was behind.
func maxDeficit(goals []store.Goal) int {
	diff, worst := 0, 0
	for _, g := range goals {
		switch g.Team {
		case "us":
			diff++
		case "them":
			diff--
		}
		worst = min(worst, diff)
	}
	return -worst
}

// winStreak counts the online wins that end the history (abandoned matches
// are skipped).
func winStreak(ms []*store.Match) int {
	sorted := append([]*store.Match(nil), ms...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].StartedAt.Before(sorted[j].StartedAt) })
	n := 0
	for i := len(sorted) - 1; i >= 0; i-- {
		m := sorted[i]
		if !m.Online || m.Result == "abandoned" {
			continue
		}
		if m.Result != "win" {
			break
		}
		n++
	}
	return n
}

// Run closes the finished sessions and posts the weekly recap until ctx ends.
func (d *Discord) Run(ctx context.Context, st *store.Store) {
	d.loadState()
	if d.weekly.IsZero() { // first start: the next recap is next Monday's
		d.weekly = lastWeeklySlot(time.Now())
		d.saveState()
	}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		now := time.Now()
		d.closeSessions(ctx, st, now)
		d.weeklyRecap(ctx, st, now)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// closeSessions posts the recap of the sessions without a match for
// tilt.SessionGap.
func (d *Discord) closeSessions(ctx context.Context, st *store.Store, now time.Time) {
	d.mu.Lock()
	var done []*openSession
	for id, s := range d.sessions {
		if now.Sub(s.last) >= tilt.SessionGap {
			done = append(done, s)
			delete(d.sessions, id)
		}
	}
	d.mu.Unlock()
	for _, s := range done {
		c, err := UserSettings(ctx, st, s.user.ID)
		if err != nil || c.DiscordOff {
			continue
		}
		ms, err := st.User(s.user.ID).List(ctx)
		if err != nil {
			continue
		}
		if e, ok := d.sessionRecap(s.user, ms, s.last); ok {
			d.send(e)
		}
	}
}

// sessionRecap sums up the online session that ended at last (3 matches
// at least).
func (d *Discord) sessionRecap(u *store.User, ms []*store.Match, last time.Time) (embed, bool) {
	var on []*store.Match
	for _, m := range ms {
		if m.Online && !m.EndedAt.After(last) {
			on = append(on, m)
		}
	}
	sort.Slice(on, func(i, j int) bool { return on[i].StartedAt.Before(on[j].StartedAt) })
	start := len(on) - 1
	for start > 0 && on[start].StartedAt.Sub(on[start-1].EndedAt) <= tilt.SessionGap {
		start--
	}
	if start < 0 {
		return embed{}, false
	}
	sess := on[start:]
	if len(sess) < 3 {
		return embed{}, false
	}
	var w, l, diff, goals, mvps int
	for _, m := range sess {
		switch m.Result {
		case "win":
			w++
		case "loss":
			l++
		}
		diff += m.TeamScore - m.OppScore
		goals += m.Me.Goals
		if m.MVP {
			mvps++
		}
	}
	rate := 0
	if w+l > 0 {
		rate = int(math.Round(float64(w) * 100 / float64(w+l)))
	}
	return embed{
		Title:       d.t("discord.sessionTitle", displayName(u)),
		Description: d.t("discord.sessionText", len(sess), w, l, rate, signed(diff), goals, mvps),
		URL:         d.playerURL(u),
		Color:       colorInfo,
	}, true
}

func signed(n int) string {
	if n > 0 {
		return fmt.Sprintf("+%d", n)
	}
	return fmt.Sprint(n)
}

// weeklyRecap posts last week's leaderboard once, on Monday from WeeklyAt.
func (d *Discord) weeklyRecap(ctx context.Context, st *store.Store, now time.Time) {
	due := lastWeeklySlot(now)
	d.mu.Lock()
	posted := !d.weekly.Before(due)
	d.mu.Unlock()
	if posted {
		return
	}
	e, ok, err := d.weeklyEmbed(ctx, st, due)
	if err != nil {
		if d.Log != nil {
			d.Log.Warn("weekly recap", "err", err)
		}
		return
	}
	if ok {
		if err := d.post(ctx, e); err != nil {
			if d.Log != nil {
				d.Log.Warn("discord post failed", "title", e.Title, "err", err)
			}
			return // retried next minute
		}
	}
	d.mu.Lock()
	d.weekly = due
	d.mu.Unlock()
	d.saveState()
}

// lastWeeklySlot is the latest Monday WeeklyAt:00 not after now.
func lastWeeklySlot(now time.Time) time.Time {
	days := (int(now.Weekday()) + 6) % 7 // days since Monday
	t := time.Date(now.Year(), now.Month(), now.Day()-days, WeeklyAt, 0, 0, 0, now.Location())
	if t.After(now) {
		t = t.AddDate(0, 0, -7)
	}
	return t
}

// weeklyEmbed ranks the players of the 7 days before slot by win rate (5
// decided matches at least).
func (d *Discord) weeklyEmbed(ctx context.Context, st *store.Store, slot time.Time) (embed, bool, error) {
	rows, err := st.Leaderboard(ctx, store.LeaderboardQuery{Since: slot.AddDate(0, 0, -7)})
	if err != nil {
		return embed{}, false, err
	}
	type line struct {
		name      string
		w, l      int
		rate, gda float64
	}
	var ls []line
	for _, r := range rows {
		if r.Wins+r.Losses < 5 {
			continue
		}
		c, err := UserSettings(ctx, st, r.UserID)
		if err != nil || c.DiscordOff {
			continue
		}
		u, err := st.UserByID(ctx, r.UserID)
		if err != nil {
			continue
		}
		ls = append(ls, line{displayName(u), r.Wins, r.Losses, float64(r.Wins) / float64(r.Wins+r.Losses), r.GoalDiffAvg})
	}
	if len(ls) == 0 {
		return embed{}, false, nil
	}
	sort.Slice(ls, func(i, j int) bool {
		if ls[i].rate != ls[j].rate {
			return ls[i].rate > ls[j].rate
		}
		return ls[i].w+ls[i].l > ls[j].w+ls[j].l
	})
	medals := []string{"🥇", "🥈", "🥉"}
	var b strings.Builder
	for i, x := range ls {
		rank := fmt.Sprintf("%d.", i+1)
		if i < len(medals) {
			rank = medals[i]
		}
		fmt.Fprintf(&b, "%s **%s** — %s\n", rank, x.name,
			d.t("discord.weeklyLine", int(math.Round(x.rate*100)), x.w, x.l, fmt.Sprintf("%+.1f", x.gda)))
	}
	return embed{Title: d.t("discord.weeklyTitle"), Description: b.String(), URL: strings.TrimRight(d.PublicURL, "/") + "/#/players", Color: colorGold}, true, nil
}

func (d *Discord) loadState() {
	if d.StateFile == "" {
		return
	}
	b, err := os.ReadFile(d.StateFile)
	if err != nil {
		return
	}
	var s struct {
		Weekly time.Time `json:"weekly"`
	}
	if json.Unmarshal(b, &s) == nil {
		d.mu.Lock()
		d.weekly = s.Weekly
		d.mu.Unlock()
	}
}

func (d *Discord) saveState() {
	if d.StateFile == "" {
		return
	}
	d.mu.Lock()
	b, _ := json.Marshal(map[string]time.Time{"weekly": d.weekly})
	d.mu.Unlock()
	if err := os.WriteFile(d.StateFile, b, 0o644); err != nil && d.Log != nil {
		d.Log.Warn("discord state", "err", err)
	}
}
