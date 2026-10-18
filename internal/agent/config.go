package agent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	"rocket-tracker/internal/setup"
)

// ErrConfigChanged rejects a reply from an earlier setup, including a setup
// that kept the same credentials but changed settings. It contains no secrets.
var ErrConfigChanged = errors.New("agent configuration changed; discard stale heartbeat and reload")

func cloneSettings(s Settings) Settings {
	s.PlayerNames = slices.Clone(s.PlayerNames)
	s.PlayerIDs = slices.Clone(s.PlayerIDs)
	return s
}

func cloneConfig(c *Config) *Config {
	v := *c
	v.Settings = cloneSettings(c.Settings)
	if v.RLPort <= 0 || v.RLPort > 65535 {
		v.RLPort = setup.DefaultPort
	}
	return &v
}

// The lock has its own stable inode: locking agent.json would stop protecting
// the path as soon as a rename replaced it. Never unlink this companion file.
// Closing the handle (including process exit/crash) releases the OS lock.
func withConfigLock(path string, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := lockConfigFile(f, false); err != nil {
		return fmt.Errorf("lock agent configuration: %w", err)
	}
	defer unlockConfigFile(f)
	return fn()
}

// LoadConfig reads a complete snapshot, serialized with cooperating writers.
func LoadConfig(path string) (*Config, error) {
	var c *Config
	err := withConfigLock(path, func() (err error) { c, err = readConfig(path); return err })
	return c, err
}

func readConfig(path string) (*Config, error) {
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
	return cloneConfig(c), nil
}

// SaveConfig starts a new setup generation even for identical credentials.
// The caller owns c; on success it receives the persisted generation. Agent.New
// takes a private copy, so subsequent setup writes cannot mutate a running agent.
func SaveConfig(path string, c *Config) error {
	next := cloneConfig(c)
	err := withConfigLock(path, func() error {
		var generation [16]byte
		if _, err := rand.Read(generation[:]); err != nil {
			return err
		}
		next.Generation = hex.EncodeToString(generation[:])
		return writeConfig(path, next)
	})
	if err == nil {
		*c = *next
	}
	return err
}

// RemoveConfig participates in the same protocol: a background reply cannot
// recreate credentials after a cooperating disconnect removed them.
func RemoveConfig(path string) error {
	return withConfigLock(path, func() error {
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	})
}

// updateConfig compares and patches the CURRENT file under one cross-process
// lock, held through rename. The generation belongs to setup; background
// patches retain it and must not replace credentials from an old snapshot.
func updateConfig(path string, expected *Config, patch func(*Config)) (*Config, error) {
	var next *Config
	err := withConfigLock(path, func() error {
		current, err := readConfig(path)
		if errors.Is(err, ErrNotConfigured) {
			return ErrConfigChanged
		}
		if err != nil {
			return err
		}
		if current.Generation != expected.Generation || current.Server != expected.Server || current.Token != expected.Token {
			return ErrConfigChanged
		}
		next = cloneConfig(current)
		patch(next)
		if reflect.DeepEqual(next, current) {
			return nil
		}
		return writeConfig(path, next)
	})
	return next, err
}

// Caller holds the companion lock. A fresh 0600 temporary file also avoids
// retaining permissive modes from an existing temporary file.
func writeConfig(path string, c *Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".agent-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
