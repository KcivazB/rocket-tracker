// Package agent sends the matches recorded on a gaming PC to a Rocket
// Tracker server (rltracker-server). Finished matches go through a local
// outbox first, so nothing is lost while the server is unreachable.
package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"rocket-tracker/internal/setup"
	"rocket-tracker/internal/store"
	"rocket-tracker/internal/tilt"
	"rocket-tracker/internal/tracker"
)

// Config is agent.json in the data directory.
type Config struct {
	Server       string `json:"server"` // e.g. https://rl.example.com
	Token        string `json:"token"`  // device token (rtk_...)
	RLInstallDir string `json:"rl_install_dir"`
	RLPort       int    `json:"rl_port"`
	// Settings is the last copy received from the server, so the tracker
	// knows who the player is even when the server is unreachable.
	Settings Settings `json:"settings"`
}

// Settings are the per-player values the tracker needs.
type Settings struct {
	PlayerNames []string `json:"player_names"`
	PlayerIDs   []string `json:"player_ids"`
	DefaultTag  string   `json:"default_tag"`
}

// ErrNotConfigured is returned by LoadConfig when agent.json is missing.
var ErrNotConfigured = errors.New("agent not configured: run `rltracker agent setup --server URL --token TOKEN`")

// LoadConfig reads agent.json.
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotConfigured
	} else if err != nil {
		return nil, err
	}
	c := &Config{}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.Server == "" || c.Token == "" {
		return nil, ErrNotConfigured
	}
	if c.RLPort <= 0 || c.RLPort > 65535 {
		c.RLPort = setup.DefaultPort
	}
	return c, nil
}

// SaveConfig writes agent.json (readable by the current user only where the
// OS supports it: it holds the device token).
func SaveConfig(path string, c *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Agent runs the heartbeat and upload loops of a gaming PC.
type Agent struct {
	Options

	mu        sync.Mutex
	cfg       *Config
	cfgMod    time.Time     // agent.json modification time last seen
	autoIdent *AutoIdentity // detected, not yet confirmed by the server
}

// AutoIdentity is the player identity the tracker detected by itself.
type AutoIdentity struct {
	Name      string `json:"name"`
	PrimaryID string `json:"primary_id"`
}

// New returns an agent for cfg.
func New(cfg *Config, o Options) *Agent {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &Agent{Options: o, cfg: cfg}
}

// Options wires an agent. Tracker may be set after New (the tracker itself
// is built with the agent's callbacks).
type Options struct {
	Client     *Client
	Outbox     *Outbox
	Tracker    *tracker.Tracker      // live match (may be nil)
	ConnStatus func() (bool, string) // game connection (may be nil)
	Ini        func() setup.IniStatus
	Version    string
	Log        *slog.Logger
	ConfigPath string // agent.json, updated with the server's settings
	OnAlert    func(*tilt.Alert) // break suggestion after a match (may be nil)
}

// Identity is the tracker's "who am I" (last settings from the server).
func (a *Agent) Identity() tracker.Identity {
	a.mu.Lock()
	defer a.mu.Unlock()
	return tracker.Identity{Names: a.cfg.Settings.PlayerNames, IDs: a.cfg.Settings.PlayerIDs}
}

// DefaultTag is the tag of new matches.
func (a *Agent) DefaultTag() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if store.ValidTag(a.cfg.Settings.DefaultTag) {
		return a.cfg.Settings.DefaultTag
	}
	return "ranked"
}

// OnAutoIdentity is the tracker callback: the identity is used locally at
// once and sent to the server with the next heartbeat.
func (a *Agent) OnAutoIdentity(name, primaryID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.cfg.Settings.PlayerNames) > 0 || len(a.cfg.Settings.PlayerIDs) > 0 {
		return
	}
	if name != "" {
		a.cfg.Settings.PlayerNames = []string{name}
	}
	if primaryID != "" {
		a.cfg.Settings.PlayerIDs = []string{primaryID}
	}
	a.autoIdent = &AutoIdentity{Name: name, PrimaryID: primaryID}
}

// Save is the tracker callback for a finished match: it goes to the outbox.
func (a *Agent) Save(m *store.Match) error {
	if err := a.Outbox.Put(NewKey(), m); err != nil {
		return err
	}
	a.Log.Info("match queued for upload", "mode", m.Mode, "result", m.Result, "guid", m.GUID)
	return nil
}

// NewKey returns a unique, time-ordered outbox key.
func NewKey() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%016x-%s", time.Now().UnixNano(), hex.EncodeToString(b))
}

// Intervals of the loops.
const (
	HeartbeatEvery = 3 * time.Second
	RetryMin       = 5 * time.Second
	RetryMax       = 5 * time.Minute
	UploadEvery    = time.Minute // safety net besides the wake-ups
)

// Run sends heartbeats and uploads the outbox until ctx is done.
func (a *Agent) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); a.heartbeatLoop(ctx) }()
	go func() { defer wg.Done(); a.uploadLoop(ctx) }()
	wg.Wait()
}

// Flush tries once to upload what is queued (used on shutdown).
func (a *Agent) Flush(ctx context.Context) {
	_, _ = a.uploadPending(ctx)
}

func (a *Agent) report() Report {
	r := Report{Version: a.Version}
	if a.ConnStatus != nil {
		r.Connected, r.Transport = a.ConnStatus()
	}
	if a.Tracker != nil {
		r.Live = a.Tracker.Live()
	}
	if a.Ini != nil {
		ini := a.Ini()
		r.Ini = &ini
	}
	a.mu.Lock()
	r.AutoIdentity = a.autoIdent
	a.mu.Unlock()
	return r
}

func (a *Agent) heartbeatLoop(ctx context.Context) {
	wait := time.Duration(0)
	reachable := true
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if a.reloadConfig() {
			reachable = true
			a.Outbox.Wake()
		}
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		rep := a.report()
		resp, err := a.Client.Heartbeat(rctx, rep)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if reachable {
				a.Log.Warn("server unreachable", "server", a.Client.ServerURL(), "err", err)
				reachable = false
			}
			if errors.Is(err, ErrUnauthorized) {
				wait = RetryMax
			} else {
				wait = 15 * time.Second
			}
			continue
		}
		if !reachable {
			a.Log.Info("server reachable again", "server", a.Client.ServerURL())
			reachable = true
			a.Outbox.Wake()
		}
		a.applySettings(resp.Settings, rep.AutoIdentity != nil)
		wait = HeartbeatEvery
	}
}

// reloadConfig picks up a new server or token written by `agent setup` while
// the agent runs. It reports whether the target changed.
func (a *Agent) reloadConfig() bool {
	if a.ConfigPath == "" {
		return false
	}
	fi, err := os.Stat(a.ConfigPath)
	if err != nil {
		return false
	}
	a.mu.Lock()
	seen := a.cfgMod
	a.cfgMod = fi.ModTime()
	a.mu.Unlock()
	if seen.IsZero() || fi.ModTime().Equal(seen) {
		return false
	}
	c, err := LoadConfig(a.ConfigPath)
	if err != nil {
		a.Log.Warn("agent.json changed but cannot be read", "err", err)
		return false
	}
	if server, token := a.Client.target(); c.Server == server && c.Token == token {
		return false
	}
	a.Client.SetTarget(c.Server, c.Token)
	a.mu.Lock()
	a.cfg.Server, a.cfg.Token = c.Server, c.Token
	a.mu.Unlock()
	a.Log.Info("agent configuration reloaded", "server", c.Server)
	return true
}

// applySettings keeps the server's settings (and saves them when they change).
func (a *Agent) applySettings(s Settings, identitySent bool) {
	a.mu.Lock()
	if identitySent {
		a.autoIdent = nil
	}
	changed := !equalSettings(a.cfg.Settings, s)
	a.cfg.Settings = s
	cfg := *a.cfg
	a.mu.Unlock()
	if changed && a.ConfigPath != "" {
		if err := SaveConfig(a.ConfigPath, &cfg); err != nil {
			a.Log.Warn("could not save agent settings", "err", err)
		} else if fi, err := os.Stat(a.ConfigPath); err == nil {
			a.mu.Lock()
			a.cfgMod = fi.ModTime() // our own write is not a reload
			a.mu.Unlock()
		}
	}
}

func equalSettings(x, y Settings) bool {
	return x.DefaultTag == y.DefaultTag && strings.Join(x.PlayerNames, "\x00") == strings.Join(y.PlayerNames, "\x00") &&
		strings.Join(x.PlayerIDs, "\x00") == strings.Join(y.PlayerIDs, "\x00")
}

func (a *Agent) uploadLoop(ctx context.Context) {
	backoff := time.Duration(0)
	for {
		wait := UploadEvery
		if backoff > 0 {
			wait = backoff
		}
		select {
		case <-ctx.Done():
			return
		case <-a.Outbox.Woken():
		case <-time.After(wait):
		}
		sent, err := a.uploadPending(ctx)
		if sent > 0 {
			a.Log.Info("matches uploaded", "count", sent)
		}
		switch {
		case err == nil:
			backoff = 0
		case ctx.Err() != nil:
			return
		default:
			backoff = min(max(backoff*2, RetryMin), RetryMax)
			a.Log.Warn("upload failed, will retry", "in", backoff.String(), "err", err)
		}
	}
}

// uploadPending sends the queued matches, oldest first. It stops at the
// first transient error; a match the server rejects is set aside.
func (a *Agent) uploadPending(ctx context.Context) (int, error) {
	items, err := a.Outbox.Pending()
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, it := range items {
		key, m, err := a.Outbox.Load(it)
		if err != nil {
			a.Log.Error("unreadable outbox entry set aside", "file", it, "err", err)
			_ = a.Outbox.Reject(it)
			continue
		}
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		alert, err := a.Client.SendMatch(rctx, key, m)
		cancel()
		if err == nil && alert != nil && a.OnAlert != nil {
			go a.OnAlert(alert)
		}
		var rej *RejectedError
		switch {
		case errors.As(err, &rej):
			a.Log.Error("server rejected a match, set aside", "file", it, "err", err)
			_ = a.Outbox.Reject(it)
		case err != nil:
			return sent, err
		default:
			sent++
			if err := a.Outbox.Remove(it); err != nil {
				return sent, err
			}
		}
	}
	return sent, nil
}
