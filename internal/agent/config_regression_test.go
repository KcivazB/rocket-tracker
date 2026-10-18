package agent

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"rocket-tracker/internal/domain"
)

type configTransport func(*http.Request) (*http.Response, error)

func (f configTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type configJournal struct{ reads int }

func (j *configJournal) PendingDeliveries(context.Context, domain.Destination) ([]domain.Delivery, error) {
	j.reads++
	return nil, nil
}
func (*configJournal) CompleteDelivery(context.Context, domain.Delivery, bool) error { return nil }

func awaitConfigTest(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("configuration test did not reach its synchronization point")
	}
}

// The transport suspends A's reply while a separate setup commits B. Cancellation
// ends the heartbeat loop after that one reply, without waiting for its timer.
func TestConfigStaleHeartbeatAfterSetup(t *testing.T) {
	for _, journal := range []bool{false, true} {
		for _, sameTarget := range []bool{false, true} {
			name := "settings"
			if journal {
				name = "journal"
			}
			if sameTarget {
				name += "/same-target-new-settings"
			} else {
				name += "/other-target"
			}
			t.Run(name, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "agent.json")
				old := &Config{Server: "https://a.invalid", Token: "synthetic-a", Settings: Settings{PlayerNames: []string{"a"}}}
				if err := SaveConfig(path, old); err != nil {
					t.Fatal(err)
				}
				next := &Config{Server: "https://b.invalid", Token: "synthetic-b", ServerID: "b", AccountID: 2, Settings: Settings{PlayerNames: []string{"b"}, DefaultTag: "casual"}}
				if sameTarget {
					next.Server, next.Token = old.Server, old.Token
				}
				entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var once sync.Once
				defer once.Do(func() { close(release) })
				client := &Client{Server: old.Server, Token: old.Token, HTTP: &http.Client{Transport: configTransport(func(*http.Request) (*http.Response, error) {
					close(entered)
					<-release
					if !journal {
						cancel()
					}
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"server_id":"a","user":{"id":1},"settings":{"player_names":["old-response"],"default_tag":"ranked"}}`))}, nil
				})}}
				j := &configJournal{}
				opts := Options{Client: client, ConfigPath: path}
				if journal {
					opts.Journal = j
				}
				a := New(old, opts)
				go func() {
					defer close(done)
					if journal {
						_, _ = a.uploadPending(ctx)
					} else {
						a.heartbeatLoop(ctx)
					}
				}()
				awaitConfigTest(t, entered)
				if err := SaveConfig(path, next); err != nil {
					t.Fatal(err)
				}
				want, err := LoadConfig(path)
				if err != nil {
					t.Fatal(err)
				}
				once.Do(func() { close(release) })
				awaitConfigTest(t, done)
				got, err := LoadConfig(path)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatal("stale heartbeat overwrote newer setup (credentials redacted)")
				}
				if journal && j.reads != 0 {
					t.Fatal("stale heartbeat read delivery payloads")
				}
			})
		}
	}
}

func TestConfigReloadAdoptsSettingsAndClearsAutoIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	cfg := &Config{Server: "https://a.invalid", Token: "synthetic"}
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	a := New(cfg, Options{Client: &Client{Server: cfg.Server, Token: cfg.Token}, ConfigPath: path})
	a.OnAutoIdentity("old-detection", "old-id")
	cfg.Settings = Settings{PlayerNames: []string{"new-player"}, PlayerIDs: []string{"new-id"}, DefaultTag: "casual"}
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	if !a.reloadConfig() {
		t.Fatal("first reload ignored settings-only setup")
	}
	id := a.Identity()
	if !reflect.DeepEqual(id.Names, cfg.Settings.PlayerNames) || !reflect.DeepEqual(id.IDs, cfg.Settings.PlayerIDs) || a.DefaultTag() != "casual" {
		t.Fatal("reload did not atomically adopt settings")
	}
	if a.report().AutoIdentity != nil {
		t.Fatal("reload retained the previous auto identity")
	}
	if a.reloadConfig() {
		t.Fatal("unchanged configuration reported a reload")
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Settings = Settings{PlayerNames: []string{"third-player"}, PlayerIDs: []string{"third-id"}, DefaultTag: "private"}
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	if !a.reloadConfig() || a.DefaultTag() != "private" || a.Identity().Names[0] != "third-player" {
		t.Fatal("settings-only setup with unchanged mtime was ignored")
	}
}

func TestConfigAgentOwnsInputsAndIdentitySnapshots(t *testing.T) {
	cfg := &Config{Server: "https://a.invalid", Token: "synthetic", Settings: Settings{PlayerNames: []string{"original"}, PlayerIDs: []string{"id"}}}
	a := New(cfg, Options{})
	cfg.Server = "https://b.invalid"
	cfg.Settings.PlayerNames[0] = "caller-change"
	if a.Destination().Server != "https://a.invalid" || a.Identity().Names[0] != "original" {
		t.Fatal("New retained caller-owned configuration")
	}
	id := a.Identity()
	id.Names[0], id.IDs[0] = "consumer-change", "consumer-id"
	if a.Identity().Names[0] != "original" || a.Identity().IDs[0] != "id" {
		t.Fatal("Identity exposed mutable internal slices")
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 1000 {
			cfg.Server = "https://caller.invalid"
			cfg.Settings.PlayerNames[0] = "caller"
		}
	}()
	go func() {
		defer wg.Done()
		for range 1000 {
			_ = a.Destination()
			_ = a.Identity().Names[0]
		}
	}()
	wg.Wait()
}

func TestConfigReloadAndConcurrentReaders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	cfg := &Config{Server: "https://a.invalid", Token: "synthetic", Settings: Settings{PlayerNames: []string{"a"}}}
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	a := New(cfg, Options{Client: &Client{Server: cfg.Server, Token: cfg.Token}, ConfigPath: path, Journal: &configJournal{}})
	done := make(chan struct{})
	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		for {
			select {
			case <-done:
				return
			default:
				_ = a.ServerURL()
				_ = a.Destination()
				_ = a.Identity().Names[0]
				_ = a.DefaultTag()
				_ = a.report()
			}
		}
	}()
	defer func() { close(done); readers.Wait() }()
	for range 20 {
		cfg.Server, cfg.Settings.PlayerNames[0] = "https://b.invalid", "b"
		if err := SaveConfig(path, cfg); err != nil {
			t.Fatal(err)
		}
		if !a.reloadConfig() {
			t.Fatal("new setup generation was ignored")
		}
	}
	if a.ServerURL() != cfg.Server || a.Identity().Names[0] != "b" {
		t.Fatal("readers did not converge on reloaded configuration")
	}
}
