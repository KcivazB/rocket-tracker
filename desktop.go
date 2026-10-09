package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"rocket-tracker/internal/i18n"
	"rocket-tracker/internal/tilt"
	"rocket-tracker/internal/tray"
	"rocket-tracker/internal/update"
	"rocket-tracker/internal/winutil"
)

// restartExe is set once an update is installed: main starts it again, with
// the same arguments, after the app has stopped.
var restartExe string

// desktop ties the tray icon, the update checker and the one-click install.
type desktop struct {
	log     *slog.Logger
	quit    context.CancelFunc
	checker *update.Checker
	tray    *tray.Tray

	mu         sync.Mutex
	installing bool
}

// startDesktop shows the notification-area icon (open, update, quit) and
// checks for a newer release in the background. quit stops the app.
func startDesktop(ctx context.Context, quit context.CancelFunc, log *slog.Logger, openURL string) *desktop {
	d := &desktop{log: log, quit: quit}
	if exe, err := executable(); err == nil {
		update.CleanupOld(exe)
	}
	d.tray = tray.Start(tray.Options{
		Tooltip:   "Rocket Tracker " + Version,
		OpenTitle: i18n.T(lang, "tray.open"),
		OnOpen:    func() { d.open(openURL) },
		QuitTitle: i18n.T(lang, "tray.quit"),
		OnQuit: func() {
			log.Info("quit from the tray icon")
			quit()
		},
	})
	d.checker = &update.Checker{
		Current: Version,
		Log:     log.With("component", "update"),
		OnNewer: func(r update.Release) {
			d.tray.ShowUpdate(i18n.Tf(lang, "tray.update", r.Version), func() { d.installFromTray(r) })
		},
	}
	go d.checker.Run(ctx)
	return d
}

// Stop removes the icon.
func (d *desktop) Stop() { d.tray.Stop() }

// Newer is the newer release found, or nil.
func (d *desktop) Newer() *update.Release { return d.checker.Newer() }

func (d *desktop) open(u string) {
	if err := winutil.OpenURL(u); err != nil {
		d.log.Warn("cannot open the browser", "err", err)
	}
}

func (d *desktop) installFromTray(r update.Release) {
	if err := d.Install(); err != nil {
		winutil.MessageBox("Rocket Tracker", i18n.Tf(lang, "update.failed", err.Error()), true)
		d.open(r.URL)
		return
	}
	d.Restart()
}

// Install downloads the newer release over the running executable. The app
// keeps running the old version until Restart.
func (d *desktop) Install() error {
	r := d.checker.Newer()
	if r == nil {
		return errors.New("no update available")
	}
	d.mu.Lock()
	if d.installing {
		d.mu.Unlock()
		return errors.New("an update is already being installed")
	}
	d.installing = true
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.installing = false
		d.mu.Unlock()
	}()

	exe, err := executable()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	d.log.Info("installing update", "version", r.Version, "exe", exe)
	err = update.Install(ctx, nil, *r, exe, func(path string) error {
		out, err := exec.CommandContext(ctx, path, "version").Output()
		if err != nil {
			return err
		}
		if got := strings.TrimSpace(string(out)); got != r.Version {
			return fmt.Errorf("it reports version %q instead of %q", got, r.Version)
		}
		return nil
	})
	if err != nil {
		d.log.Error("update failed", "version", r.Version, "err", err)
		return err
	}
	d.mu.Lock()
	restartExe = exe
	d.mu.Unlock()
	d.log.Info("update installed, restarting", "version", r.Version)
	return nil
}

// Restart stops the app; main then starts the installed version.
func (d *desktop) Restart() { d.quit() }

// executable is the path of the running program, as installed (the file an
// update replaces).
func executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	return exe, nil
}

// restartIfUpdated starts the installed update once the app has stopped (its
// single-instance lock and database are released by then).
func restartIfUpdated() {
	if restartExe == "" {
		return
	}
	cmd := exec.Command(restartExe, os.Args[1:]...)
	if err := cmd.Start(); err != nil {
		winutil.MessageBox("Rocket Tracker", i18n.Tf(lang, "update.restartFailed", err.Error()), true)
	}
}

// Tilt shows the break suggestion of a tilt alert.
func (d *desktop) Tilt(a *tilt.Alert) {
	if a == nil {
		return
	}
	title, text := tiltMessage(a)
	d.log.Info("tilt alert", "kind", a.Kind, "losses", a.Losses, "matches", a.Matches, "win_rate", a.WinRate, "sample", a.Sample)
	if err := d.tray.Notify(title, text); err != nil {
		d.log.Warn("cannot show the notification", "err", err)
	}
}

func tiltMessage(a *tilt.Alert) (title, text string) {
	pct := func(f float64) int { return int(f*100 + 0.5) }
	if a.Kind == "long_session" {
		return i18n.Tf(lang, "tilt.longTitle", a.Matches), i18n.Tf(lang, "tilt.longText", a.Matches, pct(a.WinRate), pct(a.Baseline))
	}
	title = i18n.Tf(lang, "tilt.streakTitle", a.Losses)
	switch {
	case a.Sample < tilt.MinSample:
		text = i18n.T(lang, "tilt.streakNoData")
	case a.WinRate < a.Baseline-0.05:
		text = i18n.Tf(lang, "tilt.streakWorse", a.Losses, pct(a.WinRate), pct(a.Baseline))
	default:
		text = i18n.Tf(lang, "tilt.streakOK", a.Losses, pct(a.WinRate), pct(a.Baseline))
	}
	return title, text
}
