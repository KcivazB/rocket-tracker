// Package tray shows the notification-area icon of the desktop app: open the
// dashboard, download an update, quit.
package tray

import (
	_ "embed"
	"runtime"
	"sync"
	"time"

	"fyne.io/systray"
)

//go:embed icon.ico
var icon []byte

// Options configures the icon. Every callback runs on its own goroutine.
type Options struct {
	Tooltip   string
	OpenTitle string // "" = no "open" entry
	OnOpen    func()
	QuitTitle string
	OnQuit    func()
}

// Tray is a running icon.
type Tray struct {
	ready    chan struct{}
	done     chan struct{}
	mu       sync.Mutex
	update   *systray.MenuItem
	onUpdate func()
}

// Start shows the icon. The Windows message loop runs on its own locked
// thread, so the caller keeps its goroutine.
func Start(o Options) *Tray {
	t := &Tray{ready: make(chan struct{}), done: make(chan struct{})}
	go func() {
		runtime.LockOSThread()
		defer close(t.done)
		systray.Run(func() { t.build(o) }, nil)
	}()
	return t
}

func (t *Tray) build(o Options) {
	systray.SetIcon(icon)
	systray.SetTooltip(o.Tooltip)
	if o.OpenTitle != "" && o.OnOpen != nil {
		open := systray.AddMenuItem(o.OpenTitle, "")
		go clicks(open, o.OnOpen)
		systray.SetOnTapped(o.OnOpen) // left click on the icon
	}
	t.update = systray.AddMenuItem("", "")
	t.update.Hide()
	go clicks(t.update, func() {
		t.mu.Lock()
		f := t.onUpdate
		t.mu.Unlock()
		if f != nil {
			f()
		}
	})
	systray.AddSeparator()
	quit := systray.AddMenuItem(o.QuitTitle, "")
	go clicks(quit, o.OnQuit)
	close(t.ready)
}

func clicks(item *systray.MenuItem, f func()) {
	for range item.ClickedCh {
		go f()
	}
}

// ShowUpdate adds the "update available" entry (or retitles it).
func (t *Tray) ShowUpdate(title string, onClick func()) {
	select {
	case <-t.ready:
	case <-time.After(10 * time.Second): // the icon could not be created
		return
	}
	t.mu.Lock()
	t.onUpdate = onClick
	t.mu.Unlock()
	t.update.SetTitle(title)
	t.update.Show()
	systray.SetTooltip(title)
}

// Stop removes the icon.
func (t *Tray) Stop() {
	systray.Quit()
	select {
	case <-t.done:
	case <-time.After(2 * time.Second):
	}
}
