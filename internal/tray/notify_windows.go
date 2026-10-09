//go:build windows

package tray

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The systray library has no notifications: Notify updates its icon (window
// class "SystrayClass", icon id 100) with a balloon, which Windows 10/11 show
// as a toast.
const (
	systrayClass  = "SystrayClass"
	systrayIconID = 100
)

var (
	user32                       = windows.NewLazySystemDLL("user32.dll")
	shell32                      = windows.NewLazySystemDLL("shell32.dll")
	procFindWindowExW            = user32.NewProc("FindWindowExW")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	procShellNotifyIconW         = shell32.NewProc("Shell_NotifyIconW")
)

type notifyIconData struct {
	Size                       uint32
	Wnd                        windows.Handle
	ID, Flags, CallbackMessage uint32
	Icon                       windows.Handle
	Tip                        [128]uint16
	State, StateMask           uint32
	Info                       [256]uint16
	Timeout, Version           uint32
	InfoTitle                  [64]uint16
	InfoFlags                  uint32
	GuidItem                   windows.GUID
	BalloonIcon                windows.Handle
}

func notify(title, text string) error {
	const (
		nimModify = 0x1
		nifInfo   = 0x10
		niifUser  = 0x4 // the icon of the tray entry
		niifLarge = 0x20
	)
	wnd := ownWindow()
	if wnd == 0 {
		return errors.New("tray icon not found")
	}
	nid := notifyIconData{Wnd: wnd, ID: systrayIconID, Flags: nifInfo, InfoFlags: niifUser | niifLarge}
	nid.Size = uint32(unsafe.Sizeof(nid))
	copyUTF16(nid.InfoTitle[:], title)
	copyUTF16(nid.Info[:], text)
	r, _, err := procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
	if r == 0 {
		return err
	}
	return nil
}

// ownWindow finds this process's systray window.
func ownWindow() windows.Handle {
	cls, _ := windows.UTF16PtrFromString(systrayClass)
	pid := uint32(os.Getpid())
	var h uintptr
	for {
		h, _, _ = procFindWindowExW.Call(0, h, uintptr(unsafe.Pointer(cls)), 0)
		if h == 0 {
			return 0
		}
		var p uint32
		procGetWindowThreadProcessId.Call(h, uintptr(unsafe.Pointer(&p)))
		if p == pid {
			return windows.Handle(h)
		}
	}
}

// copyUTF16 copies s into dst, truncated, NUL-terminated.
func copyUTF16(dst []uint16, s string) {
	u, _ := windows.UTF16FromString(s)
	if len(u) > len(dst) {
		u = u[:len(dst)]
		u[len(u)-1] = 0
	}
	copy(dst, u)
}
