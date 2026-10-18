// Package agent sends the matches recorded on a gaming PC to a Rocket
// Tracker server (rltracker-server). Finished matches go through a local
// outbox first, so nothing is lost while the server is unreachable.
package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"rocket-tracker/internal/setup"
	"rocket-tracker/internal/store"
	"rocket-tracker/internal/tilt"
	"rocket-tracker/internal/tracker"
)

// Config is agent.json in the data directory.
type Config struct {
	Generation   string `json:"generation,omitempty"` // setup identity; not an authentication secret
	ServerID     string `json:"server_id,omitempty"`
	AccountID    int64  `json:"account_id,omitempty"`
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

// Agent runs the heartbeat and upload loops of a gaming PC.
type Agent struct {
	Options

	mu        sync.Mutex
	uploadMu  sync.Mutex // an upload pass vs a change of server
	cfg       *Config
	autoIdent *AutoIdentity // detected, not yet confirmed by the server
	reachable atomic.Bool   // the last heartbeat got an answer
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
	return &Agent{Options: o, cfg: cloneConfig(cfg)}
}

// Options wires an agent. Tracker may be set after New (the tracker itself
// is built with the agent's callbacks).
type Options struct {
	Journal DeliveryJournal

	Client     *Client
	Outbox     *Outbox
	Tracker    *tracker.Tracker      // live match (may be nil)
	ConnStatus func() (bool, string) // game connection (may be nil)
	Ini        func() setup.IniStatus
	Version    string
	Log        *slog.Logger
	ConfigPath string            // agent.json, updated with the server's settings
	OnAlert    func(*tilt.Alert) // break suggestion after a match (may be nil)
}

// Identity is the tracker's "who am I" (last settings from the server).
func (a *Agent) Identity() tracker.Identity {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := cloneSettings(a.cfg.Settings)
	if a.autoIdent != nil && len(s.PlayerNames) == 0 && len(s.PlayerIDs) == 0 {
		if a.autoIdent.Name != "" {
			s.PlayerNames = []string{a.autoIdent.Name}
		}
		if a.autoIdent.PrimaryID != "" {
			s.PlayerIDs = []string{a.autoIdent.PrimaryID}
		}
	}
	return tracker.Identity{Names: s.PlayerNames, IDs: s.PlayerIDs}
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

// SaveTraining is the tracker callback for a training: it goes to the outbox.
func (a *Agent) SaveTraining(t *store.Training) error {
	if err := a.Outbox.PutTraining(NewKey(), t); err != nil {
		return err
	}
	a.Log.Info("training queued for upload", "minutes", t.EndedAt.Sub(t.StartedAt).Minutes())
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
	a.Outbox.Wake() // resume already committed intents immediately after restart
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

// Reachable reports whether the server answered the last heartbeat.
func (a *Agent) Reachable() bool { return a.reachable.Load() }

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
		a.uploadMu.Lock()
		_, err := a.heartbeat(rctx)
		a.uploadMu.Unlock()
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, ErrConfigChanged) {
				// Keep the normal reload cadence even if setup was removed. A
				// missing file must not turn stale replies into a busy retry loop.
				wait = HeartbeatEvery
				continue
			}
			a.reachable.Store(false)
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
		a.reachable.Store(true)
		if !reachable {
			a.Log.Info("server reachable again", "server", a.Client.ServerURL())
			reachable = true
			a.Outbox.Wake()
		}
		wait = HeartbeatEvery
	}
}

// reloadConfig adopts the complete persisted configuration under uploadMu.
// Read each pass: mtime can be unchanged and settings can change without a new
// server/token. Local auto-detection is kept separate from persisted settings.
func (a *Agent) reloadConfig() bool {
	if a.ConfigPath == "" {
		return false
	}
	a.uploadMu.Lock()
	defer a.uploadMu.Unlock()
	c, err := LoadConfig(a.ConfigPath)
	if err != nil {
		if !errors.Is(err, ErrNotConfigured) {
			a.Log.Warn("cannot reload agent configuration", "err", err)
		}
		return false
	}
	a.mu.Lock()
	old := a.cfg
	changed := c.Generation != old.Generation || c.Server != old.Server || c.Token != old.Token ||
		c.ServerID != old.ServerID || c.AccountID != old.AccountID || c.RLInstallDir != old.RLInstallDir || c.RLPort != old.RLPort || !equalSettings(c.Settings, old.Settings)
	a.mu.Unlock()
	if !changed {
		return false
	}
	server, _ := a.Client.target()
	// Only the compatibility file library sets old queues aside. Production
	// journal intents retain their own verified destination.
	if a.Journal == nil && !sameServer(c.Server, server) {
		if _, err := a.Outbox.SetDestination(c.Server, a.Log); err != nil {
			a.Log.Error("cannot set aside the previous server queue", "err", err)
			return false
		}
	}
	a.mu.Lock()
	a.Client.SetTarget(c.Server, c.Token)
	a.cfg, a.autoIdent = c, nil
	a.mu.Unlock()
	a.Log.Info("agent configuration reloaded", "server", c.Server)
	return true
}

// ServerURL returns the current target without exposing shared configuration.
func (a *Agent) ServerURL() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.Server
}

func (a *Agent) configSnapshot() *Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return cloneConfig(a.cfg)
}

// heartbeat runs under uploadMu, from request snapshot through response adoption.
// No filesystem lock is held during the network request. Both identity and
// settings use the same generation/target CAS before changing memory or disk.
func (a *Agent) heartbeat(ctx context.Context) (*HeartbeatResponse, error) {
	expected := a.configSnapshot()
	expected.Server, expected.Token = a.Client.target()
	rep := a.report()
	hb, err := a.Client.Heartbeat(ctx, rep)
	if err != nil {
		return nil, err
	}
	if a.Journal != nil && (hb.ServerID == "" || hb.User.ID <= 0) {
		return nil, errors.New("server does not provide a verified delivery identity; upgrade the server")
	}
	patch := func(c *Config) {
		if a.Journal != nil {
			c.ServerID, c.AccountID = hb.ServerID, hb.User.ID
		}
		c.Settings = cloneSettings(hb.Settings)
	}
	var next *Config
	if a.ConfigPath != "" {
		next, err = updateConfig(a.ConfigPath, expected, patch)
		if err != nil {
			return nil, err
		}
	} else {
		next = expected
		patch(next)
	}
	a.mu.Lock()
	a.cfg = next
	if a.autoIdent == rep.AutoIdentity {
		a.autoIdent = nil
	}
	a.mu.Unlock()
	return hb, nil
}

func equalSettings(x, y Settings) bool {
	return x.DefaultTag == y.DefaultTag && slices.Equal(x.PlayerNames, y.PlayerNames) && slices.Equal(x.PlayerIDs, y.PlayerIDs)
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
	a.uploadMu.Lock()
	defer a.uploadMu.Unlock()
	if a.Journal != nil {
		return a.uploadJournal(ctx)
	}
	items, err := a.Outbox.Pending()
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, it := range items {
		e, err := a.Outbox.Load(it)
		if err != nil {
			a.Log.Error("unreadable outbox entry set aside", "file", it, "err", err)
			_ = a.Outbox.Reject(it)
			continue
		}
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		var alert *tilt.Alert
		if e.Match != nil {
			alert, err = a.Client.SendMatch(rctx, e.Key, e.Match)
		} else {
			err = a.Client.SendTraining(rctx, e.Key, e.Training)
		}
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
