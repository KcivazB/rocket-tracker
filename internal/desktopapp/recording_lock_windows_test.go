package desktopapp

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func probeRecordingLock(path string) error {
	f, err := os.OpenFile(path+".lock", os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
	if err == nil {
		_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &windows.Overlapped{})
		return fmt.Errorf("record commit did not hold setup lock")
	}
	if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return err
	}
	return nil
}
