//go:build !windows

package tray

import "errors"

func notify(title, text string) error {
	return errors.New("notifications are only supported on Windows")
}
