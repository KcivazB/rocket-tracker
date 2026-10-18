//go:build !windows && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package agent

import (
	"errors"
	"os"
)

func lockConfigFile(*os.File, bool) error {
	return errors.New("agent configuration locking unsupported on this OS")
}

func unlockConfigFile(*os.File) {}
