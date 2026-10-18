package desktopapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"rocket-tracker/internal/agent"
	"rocket-tracker/internal/domain"
	"rocket-tracker/internal/store"
)

type commitBarrier struct {
	probe   func() error
	records *store.Scope
	entered chan struct{}
	release chan struct{}
	fail    error
}

func (b *commitBarrier) RecordResult(ctx context.Context, m *domain.Match, d *domain.Destination) error {
	probeErr := b.probe()
	close(b.entered)
	<-b.release
	if probeErr != nil {
		return probeErr
	}
	if b.fail != nil {
		return b.fail
	}
	return b.records.RecordResult(ctx, m, d)
}
func (b *commitBarrier) RecordTraining(ctx context.Context, v *domain.Training, d *domain.Destination) error {
	probeErr := b.probe()
	close(b.entered)
	<-b.release
	if probeErr != nil {
		return probeErr
	}
	if b.fail != nil {
		return b.fail
	}
	return b.records.RecordTraining(ctx, v, d)
}
func awaitRecording(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("recording/setup did not finish")
		return nil
	}
}
func recordingFixture(t *testing.T) (string, *store.Scope, *agent.Config, *domain.Match) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.json")
	cfg := &agent.Config{Server: "https://same.invalid", Token: "synthetic-a", ServerID: "server", AccountID: 1}
	if err := agent.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "local.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return path, db.User(0), cfg, &domain.Match{GUID: "record", Online: true, StartedAt: at, EndedAt: at.Add(time.Minute), Result: "win"}
}

// Runs on every host, and natively on Windows CI. The barrier is inside the
// actual production guard, after destination selection and before SQLite commit.
func TestSetupRecordingCommitLinearization(t *testing.T) {
	for _, training := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			t.Run(map[bool]string{false: "match", true: "training"}[training]+map[bool]string{false: "/commit", true: "/rollback"}[fail], func(t *testing.T) {
				path, sc, a, m := recordingFixture(t)
				b := &agent.Config{Server: a.Server, Token: "synthetic-b", ServerID: a.ServerID, AccountID: 2}
				barrier := &commitBarrier{records: sc, entered: make(chan struct{}), release: make(chan struct{})}
				barrier.probe = func() error {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRecordingLockProbeProcess$")
					cmd.Env = append(os.Environ(), "ROCKET_RECORDING_LOCK_PROBE="+path)
					out, err := cmd.CombinedOutput()
					if err != nil {
						return fmt.Errorf("crossprocess recording lock: %w: %s", err, out)
					}
					return nil
				}
				sentinel := errors.New("synthetic commit failure")
				if fail {
					barrier.fail = sentinel
				}
				rec := configuredRecorder(barrier, path)
				committed := make(chan error, 1)
				go func() {
					if training {
						committed <- rec.RecordTraining(context.Background(), &domain.Training{StartedAt: m.StartedAt, EndedAt: m.EndedAt, Source: "timer"})
					} else {
						committed <- rec.RecordResult(context.Background(), m)
					}
				}()
				select {
				case <-barrier.entered:
				case <-time.After(10 * time.Second):
					t.Fatal("commit not entered")
				}
				setupStarted, setupDone := make(chan struct{}), make(chan error, 1)
				go func() { close(setupStarted); setupDone <- agent.SaveConfig(path, b) }()
				<-setupStarted
				// A confirmed setup cannot appear while the commit's selected generation
				// is protected. No elapsed-time delay is used as the success oracle.
				select {
				case err := <-setupDone:
					t.Fatalf("setup overtook protected commit: %v", err)
				default:
				}
				close(barrier.release)
				err := awaitRecording(t, committed)
				if fail && !errors.Is(err, sentinel) || !fail && err != nil {
					t.Fatal(err)
				}
				if err := awaitRecording(t, setupDone); err != nil {
					t.Fatal(err)
				}
				// Setup B is now confirmed; all subsequent records select B from disk.
				next := configuredRecorder(sc, path)
				m.GUID = "after-setup"
				if err := next.RecordResult(context.Background(), m); err != nil {
					t.Fatal(err)
				}
				pending, err := sc.PendingDeliveries(context.Background(), domain.Destination{Server: b.Server, ServerID: b.ServerID, AccountID: b.AccountID})
				if err != nil || len(pending) != 1 || pending[0].Match.GUID != "after-setup" {
					t.Fatal(pending, err)
				}
				old, err := sc.PendingDeliveries(context.Background(), domain.Destination{Server: a.Server, ServerID: a.ServerID, AccountID: a.AccountID})
				want := 1
				if fail {
					want = 0
				}
				if err != nil || len(old) != want {
					t.Fatal(old, err)
				}
			})
		}
	}
}

func TestConfiguredRecorderMissingAndInvalidSetup(t *testing.T) {
	path, sc, _, m := recordingFixture(t)
	if err := agent.RemoveConfig(path); err != nil {
		t.Fatal(err)
	}
	rec := configuredRecorder(sc, path)
	if err := rec.RecordResult(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{`{`, `{}`, `{"server":"https://same.invalid"}`} {
		if err := os.WriteFile(path, []byte(payload), 0600); err != nil {
			t.Fatal(err)
		}
		m.GUID = "invalid-setup"
		if err := rec.RecordResult(context.Background(), m); err == nil {
			t.Fatal("invalid existing setup became local-only")
		}
	}
	status, err := sc.DeliveryStatus(context.Background())
	if err != nil || status.Pending != 0 || status.Held != 0 {
		t.Fatal(status, err)
	}
	if count, err := sc.Count(context.Background()); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}

func TestRecordingLockProbeProcess(t *testing.T) {
	path := os.Getenv("ROCKET_RECORDING_LOCK_PROBE")
	if path == "" {
		return
	}
	if err := probeRecordingLock(path); err != nil {
		t.Fatal(err)
	}
}
