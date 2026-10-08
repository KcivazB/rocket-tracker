//go:build windows

// Package winutil wraps the few Win32 calls the app needs.
package winutil

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// AutostartName is the HKCU Run value name.
const AutostartName = "RocketTracker"

// SetAutostart registers command under HKCU\...\Run.
func SetAutostart(command string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(AutostartName, command)
}

// GetAutostart returns the registered command ("" if none).
func GetAutostart() string {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue(AutostartName)
	if err != nil {
		return ""
	}
	return v
}

// RemoveAutostart deletes the Run value (no error if absent).
func RemoveAutostart() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	defer k.Close()
	err = k.DeleteValue(AutostartName)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	return err
}

// OpenURL opens a URL (or file) with the default handler.
func OpenURL(u string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(u)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}

// shellExecuteInfo mirrors SHELLEXECUTEINFOW.
type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         windows.Handle
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     windows.Handle
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    windows.Handle
	dwHotKey     uint32
	hIconOrMon   windows.Handle
	hProcess     windows.Handle
}

const seeMaskNoCloseProcess = 0x00000040

var (
	shell32            = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteEx = shell32.NewProc("ShellExecuteExW")
	kernel32           = windows.NewLazySystemDLL("kernel32.dll")
	procAttachConsole  = kernel32.NewProc("AttachConsole")
	procGetConsoleWnd  = kernel32.NewProc("GetConsoleWindow")
)

// RunElevated starts exe with args through the UAC "runas" verb, waits for
// it and returns its exit code.
func RunElevated(exe string, args []string) (int, error) {
	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return -1, err
	}
	params, err := windows.UTF16PtrFromString(joinArgs(args))
	if err != nil {
		return -1, err
	}
	cwd, _ := os.Getwd()
	dir, _ := windows.UTF16PtrFromString(cwd)
	info := shellExecuteInfo{
		fMask:        seeMaskNoCloseProcess,
		lpVerb:       verb,
		lpFile:       file,
		lpParameters: params,
		lpDirectory:  dir,
		nShow:        windows.SW_HIDE,
	}
	info.cbSize = uint32(unsafe.Sizeof(info))
	r, _, callErr := procShellExecuteEx.Call(uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		if errno, ok := callErr.(syscall.Errno); ok && errno == windows.ERROR_CANCELLED {
			return -1, errors.New("elevation cancelled by user")
		}
		return -1, fmt.Errorf("ShellExecuteEx runas: %w", callErr)
	}
	if info.hProcess == 0 {
		return 0, nil
	}
	defer windows.CloseHandle(info.hProcess)
	if _, err := windows.WaitForSingleObject(info.hProcess, windows.INFINITE); err != nil {
		return -1, err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.hProcess, &code); err != nil {
		return -1, err
	}
	return int(code), nil
}

func joinArgs(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = windows.EscapeArg(a)
	}
	return strings.Join(q, " ")
}

// IsElevated reports whether the process token is elevated.
func IsElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// AttachParentConsole attaches to the console of the parent process (when
// started from a terminal) so a -H windowsgui binary can print. It only
// replaces std handles that are not already valid (redirections win).
// Returns true when output goes to a console or a redirection.
func AttachParentConsole() bool {
	if hasConsole() {
		return true
	}
	stdoutOK := validHandle(windows.STD_OUTPUT_HANDLE)
	stderrOK := validHandle(windows.STD_ERROR_HANDLE)
	const attachParent = uintptr(0xFFFFFFFF) // ATTACH_PARENT_PROCESS = (DWORD)-1
	r, _, _ := procAttachConsole.Call(attachParent)
	if r == 0 {
		return stdoutOK || stderrOK
	}
	if !stdoutOK {
		if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
			os.Stdout = f
		}
	}
	if !stderrOK {
		if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
			os.Stderr = f
		}
	}
	// The shell prompt was already printed; start on a fresh line.
	if !stdoutOK {
		fmt.Fprintln(os.Stdout)
	}
	return true
}

func hasConsole() bool {
	r, _, _ := procGetConsoleWnd.Call()
	return r != 0
}

func validHandle(which uint32) bool {
	h, err := windows.GetStdHandle(which)
	if err != nil || h == 0 || h == windows.InvalidHandle {
		return false
	}
	t, err := windows.GetFileType(h)
	return err == nil && t != windows.FILE_TYPE_UNKNOWN
}

// MessageBox shows a simple message box.
func MessageBox(title, text string, isError bool) {
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString(title)
	var flags uint32 = windows.MB_OK | windows.MB_SETFOREGROUND
	if isError {
		flags |= windows.MB_ICONERROR
	} else {
		flags |= windows.MB_ICONINFORMATION
	}
	_, _ = windows.MessageBox(0, t, c, flags)
}

// SingleInstance creates a named mutex. ok=false when another instance
// already holds it. Keep the returned handle alive for the process lifetime.
func SingleInstance(name string) (release func(), ok bool, err error) {
	n, err := windows.UTF16PtrFromString(`Local\` + name)
	if err != nil {
		return func() {}, false, err
	}
	h, err := windows.CreateMutex(nil, false, n)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if h != 0 {
			windows.CloseHandle(h)
		}
		return func() {}, false, nil
	}
	if err != nil {
		return func() {}, false, err
	}
	return func() { windows.CloseHandle(h) }, true, nil
}

var procGetUserDefaultUILanguage = kernel32.NewProc("GetUserDefaultUILanguage")

// UILanguageIsFrench reports whether the Windows display language is French
// (any region): primary language id LANG_FRENCH = 0x0c.
func UILanguageIsFrench() bool {
	r, _, _ := procGetUserDefaultUILanguage.Call()
	return r&0x3ff == 0x0c
}
