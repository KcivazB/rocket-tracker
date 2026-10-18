package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestSharedRevisionIncludesOtherAccounts(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "shared.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	a, b := s.User(1), s.User(2)
	at := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	mk := func(profile string) *Match {
		return &Match{GUID: "shared-revision", Online: true, StartedAt: at, EndedAt: at.Add(time.Minute), Result: "win", Me: MeStats{PrimaryID: profile}}
	}
	if err = a.Ingest(ctx, mk("a"), "a"); err != nil {
		t.Fatal(err)
	}
	personal, shared := a.DataRev(), a.SharedRev()
	m := mk("b")
	if err = b.Ingest(ctx, m, "b"); err != nil {
		t.Fatal(err)
	}
	if a.DataRev() != personal || a.SharedRev() == shared {
		t.Fatal("other account did not invalidate only shared population")
	}
	rows, err := a.Shared(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("shared=%v %v", rows, err)
	}
	shared = a.SharedRev()
	if err = b.Delete(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if a.SharedRev() == shared {
		t.Fatal("delete did not invalidate")
	}
	rows, err = a.Shared(ctx)
	if err != nil || len(rows) != 0 {
		t.Fatalf("after delete=%v %v", rows, err)
	}
}
