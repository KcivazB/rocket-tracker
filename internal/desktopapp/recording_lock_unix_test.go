//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package desktopapp

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func probeRecordingLock(path string) error {
	f, err := os.OpenFile(path+".lock", os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		return fmt.Errorf("record commit did not hold setup lock")
	}
	if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
		return err
	}
	return nil
}
