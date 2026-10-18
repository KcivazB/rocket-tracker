//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package agent

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockConfigFile(f *os.File, nonblocking bool) error {
	flags := unix.LOCK_EX
	if nonblocking {
		flags |= unix.LOCK_NB
	}
	for {
		err := unix.Flock(int(f.Fd()), flags)
		if err != unix.EINTR {
			return err
		}
	}
}

func unlockConfigFile(f *os.File) { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }
