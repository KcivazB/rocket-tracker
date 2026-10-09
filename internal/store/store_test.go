package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestStoreCRUD(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "t.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 10, 1, 20, 0, 0, 0, time.UTC)
	a := &Match{GUID: "A", Online: true, StartedAt: t0.Add(time.Hour), EndedAt: t0.Add(time.Hour + 6*time.Minute), Result: "win", Tag: "ranked", TeamScore: 3, OppScore: 1,
		Goals: []Goal{{T: 12, Team: "them"}, {T: 40, Team: "us"}}}
	b := &Match{GUID: "local-1", StartedAt: t0, EndedAt: t0.Add(5 * time.Minute), Result: "loss", Tag: "ranked"}
	if err := s.Save(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(ctx, b); err != nil {
		t.Fatal(err)
	}
	// Same online guid saved again => replaced, not duplicated.
	a2 := *a
	a2.ID = 0
	a2.TeamScore = 4
	if err := s.Save(ctx, &a2); err != nil || a2.ID != a.ID {
		t.Fatalf("upsert id %d vs %d err %v", a2.ID, a.ID, err)
	}
	ms, err := s.List(ctx)
	if err != nil || len(ms) != 2 {
		t.Fatalf("list %d %v", len(ms), err)
	}
	if ms[0].GUID != "local-1" || ms[1].GUID != "A" { // oldest first
		t.Fatalf("order %s %s", ms[0].GUID, ms[1].GUID)
	}
	m := ms[1]
	if m.TeamScore != 4 || m.GoalDiff != 3 || m.FirstGoal != "them" || m.Statfeed == nil || m.Players == nil {
		t.Fatalf("%+v", m)
	}
	if ms[0].FirstGoal != "none" || ms[0].Movement != nil || ms[0].Hits != nil {
		t.Fatalf("%+v", ms[0])
	}
	if got, err := s.SetTag(ctx, m.ID, "casual"); err != nil || got.Tag != "casual" {
		t.Fatalf("settag %v %v", got, err)
	}
	if g, _ := s.Get(ctx, m.ID); g.Tag != "casual" {
		t.Fatal("tag not persisted")
	}
	if err := s.Delete(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, m.ID); err != ErrNotFound {
		t.Fatalf("expected not found, got %v", err)
	}
	if n, _ := s.Count(ctx); n != 1 {
		t.Fatalf("count %d", n)
	}
	s.Close()

	// Reopen: migrations are idempotent and data persists.
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n, _ := s.Count(ctx); n != 1 {
		t.Fatalf("count after reopen %d", n)
	}
}

// Regression: re-saving the same online guid (reconnect after a crash, or a
// stale restart) reset the user's tag and start time, and an "abandoned"
// record could overwrite the real result.
func TestUpsertKeepsTagStartAndResult(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "u.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	t0 := time.Date(2026, 10, 1, 20, 0, 0, 0, time.UTC)
	first := &Match{GUID: "G", Online: true, StartedAt: t0, EndedAt: t0.Add(3 * time.Minute), Result: "abandoned", Tag: "ranked"}
	if err := s.Save(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetTag(ctx, first.ID, "tournament"); err != nil {
		t.Fatal(err)
	}
	resumed := &Match{GUID: "G", Online: true, StartedAt: t0.Add(5 * time.Minute), EndedAt: t0.Add(9 * time.Minute), Result: "win", Tag: "ranked"}
	if err := s.Save(ctx, resumed); err != nil {
		t.Fatal(err)
	}
	m, err := s.Get(ctx, first.ID)
	if err != nil || m.Tag != "tournament" || !m.StartedAt.Equal(t0) || m.Result != "win" {
		t.Fatalf("after resume %+v %v", m, err)
	}
	stale := &Match{GUID: "G", Online: true, StartedAt: t0.Add(10 * time.Minute), EndedAt: t0.Add(12 * time.Minute), Result: "abandoned", Tag: "ranked"}
	if err := s.Save(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.Get(ctx, first.ID); m.Result != "win" {
		t.Fatalf("abandoned overwrote the result: %+v", m)
	}
	if n, _ := s.Count(ctx); n != 1 {
		t.Fatalf("count %d", n)
	}
}

// Regression: '#' and '%' in the data dir (e.g. a Windows user name) broke the
// SQLite "file:" URI.
func TestOpenPathWithURIChars(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "user #1 100%")
	path := filepath.Join(dir, "rl.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(context.Background(), &Match{GUID: "x", StartedAt: time.Now(), EndedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("db not created at the expected path: %v", err)
	}
}

// Server reads while the tracker writes must neither fail nor deadlock.
func TestConcurrentReadWrite(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				m := &Match{GUID: fmt.Sprintf("g%d-%d", i, j), Online: true, StartedAt: time.Now(), EndedAt: time.Now(), Result: "win"}
				if err := s.Save(ctx, m); err != nil {
					errs <- err
				}
			}
		}(i)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				if _, err := s.List(ctx); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if n, _ := s.Count(ctx); n != 100 {
		t.Fatalf("count %d", n)
	}
}

// The desktop app is killed rather than closed: after a checkpoint, the .db
// file alone (copied without its -wal) must hold every match.
func TestCheckpointKeepsDBFileComplete(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "t.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 3; i++ {
		m := &Match{GUID: fmt.Sprintf("G%d", i), Online: true, StartedAt: time.Date(2026, 10, 1, 20, i, 0, 0, time.UTC), Result: "win"}
		if err := s.Save(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path + "-wal"); err == nil && fi.Size() != 0 {
		t.Fatalf("wal not truncated: %d bytes", fi.Size())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cp := filepath.Join(dir, "copy.db")
	if err := os.WriteFile(cp, data, 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Open(cp)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if n, err := c.Count(ctx); err != nil || n != 3 {
		t.Fatalf("copy has %d matches, err %v", n, err)
	}
}
