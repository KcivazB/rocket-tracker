package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"rocket-tracker/internal/config"
	"rocket-tracker/internal/store"
)

func TestDiscordCloseCancelsAndJoinsHighlights(t *testing.T) {
	entered, returned := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	d := &Discord{Client: &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		close(entered)
		<-r.Context().Done()
		defer close(returned)
		return nil, r.Context().Err()
	})}}
	d.Bind(context.Background())
	now := time.Now()
	m := &store.Match{Online: true, EndedAt: now, Result: "win", Overtime: true}
	d.Match(&store.User{ID: 1}, "https://discord.com/api/webhooks/1/synthetic", m, nil, now)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("highlight not admitted")
	}
	done := make(chan struct{})
	go func() { d.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not join")
	}
	select {
	case <-returned:
	default:
		t.Fatal("Close returned before transport")
	}
	d.Match(&store.User{ID: 1}, "https://discord.com/api/webhooks/1/synthetic", m, nil, now)
	if calls.Load() != 1 {
		t.Fatal("admitted highlight after close")
	}
}

func weeklyFixture(t *testing.T, n int) (*store.Store, time.Time) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "weekly.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	slot := lastWeeklySlot(time.Now())
	for j := 0; j < n; j++ {
		u, err := st.UpsertOIDCUser(ctx, fmt.Sprint(j), fmt.Sprintf("user%d", j), "User", "")
		if err != nil {
			t.Fatal(err)
		}
		c := config.Defaults()
		c.DiscordWebhook = fmt.Sprintf("https://discord.com/api/webhooks/%d/synthetic-secret", j+1)
		b, _ := json.Marshal(c)
		if err = st.SetUserSettings(ctx, u.ID, string(b)); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 6; i++ {
			at := slot.Add(-time.Duration(i+1) * time.Hour)
			if err = st.User(u.ID).Save(ctx, &store.Match{GUID: fmt.Sprintf("%d-%d", j, i), Online: true, StartedAt: at, EndedAt: at.Add(time.Minute), Result: "win", Tag: "ranked"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	return st, slot
}

func TestDiscordCancelledWeeklyRetriesOnlyUnsentDestinations(t *testing.T) {
	st, slot := weeklyFixture(t, 3)
	state := filepath.Join(t.TempDir(), "discord.json")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := map[string]int{}
	first := ""
	calls := 0
	d := &Discord{StateFile: state, weekly: slot.AddDate(0, 0, -7), Client: &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		attempts[r.URL.Path]++
		if calls == 1 {
			first = r.URL.Path
			return &http.Response{StatusCode: 204, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		cancel()
		return nil, context.Canceled
	})}}
	d.weeklyRecap(ctx, st, slot.Add(time.Minute))
	if !d.weekly.Before(slot) {
		t.Fatal("cancellation consumed weekly slot")
	}
	if calls != 2 {
		t.Fatalf("calls before cancellation=%d", calls)
	}
	restored := &Discord{StateFile: state, Client: &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		attempts[r.URL.Path]++
		return &http.Response{StatusCode: 204, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}}
	restored.loadState()
	restored.weeklyRecap(context.Background(), st, slot.Add(2*time.Minute))
	if restored.weekly != slot || calls != 4 || attempts[first] != 1 {
		t.Fatalf("weekly=%v calls=%d attempts=%v", restored.weekly, calls, attempts)
	}
	b, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "synthetic-secret") {
		t.Fatal("webhook leaked in checkpoint")
	}
	restored.weeklyRecap(context.Background(), st, slot.Add(3*time.Minute))
	if calls != 4 {
		t.Fatal("completed slot replayed")
	}
}
