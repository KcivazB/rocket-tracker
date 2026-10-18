package agent

import (
	"errors"
	"os"
)

// WithConfigSnapshot holds the setup lock through a caller's local commit.
// Only an absent file is local-only; malformed or incomplete setup fails closed.
func WithConfigSnapshot(path string, commit func(*Config) error) error {
	return withConfigLock(path, func() error {
		cfg, err := readConfig(path)
		if errors.Is(err, ErrNotConfigured) {
			if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
				return commit(nil)
			}
		}
		if err != nil {
			return err
		}
		return commit(cfg)
	})
}
