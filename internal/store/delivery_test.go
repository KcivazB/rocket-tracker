package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"rocket-tracker/internal/domain"
	"testing"
	"time"
)

func deliveryFixture(t *testing.T) (context.Context, *Store, *Scope, domain.Destination, *Match) {
	t.Helper()
	ctx := context.Background()
	s, e := Open(filepath.Join(t.TempDir(), "local.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	d := domain.Destination{Server: "https://server.invalid", ServerID: "installation-A", AccountID: 1}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return ctx, s, s.User(0), d, &Match{GUID: "game", Online: true, StartedAt: at, EndedAt: at.Add(time.Minute), Result: "win", Tag: "ranked"}
}
func TestRecordIntentAtomicAndRevisionAcknowledgement(t *testing.T) {
	ctx, s, sc, d, m := deliveryFixture(t)
	if _, e := s.db.Exec(`CREATE TRIGGER fail_intent BEFORE INSERT ON delivery_intents BEGIN SELECT RAISE(ABORT,'injected journal failure'); END`); e != nil {
		t.Fatal(e)
	}
	if e := sc.RecordResult(ctx, m, &d); e == nil {
		t.Fatal("record succeeded without intent")
	}
	if n, _ := sc.Count(ctx); n != 0 {
		t.Fatal("partial local write", n)
	}
	if _, e := s.db.Exec(`DROP TRIGGER fail_intent`); e != nil {
		t.Fatal(e)
	}
	if e := sc.RecordResult(ctx, m, &d); e != nil {
		t.Fatal(e)
	}
	first, e := sc.PendingDeliveries(ctx, d)
	if e != nil || len(first) != 1 {
		t.Fatalf("pending %v %v", first, e)
	}
	if e := sc.RecordResult(ctx, m, &d); e != nil {
		t.Fatal(e)
	}
	same, _ := sc.PendingDeliveries(ctx, d)
	if same[0].Revision != first[0].Revision {
		t.Fatal("identical replay changed revision")
	}
	m.TeamScore++
	if e := sc.RecordResult(ctx, m, &d); e != nil {
		t.Fatal(e)
	}
	newer, _ := sc.PendingDeliveries(ctx, d)
	if newer[0].Revision <= first[0].Revision || newer[0].Key != first[0].Key {
		t.Fatal("revision/key contract")
	}
	if e := sc.CompleteDelivery(ctx, first[0], false); e != nil {
		t.Fatal(e)
	}
	pending, _ := sc.PendingDeliveries(ctx, d)
	if len(pending) != 1 || pending[0].Match.TeamScore != 1 {
		t.Fatal("late ack removed newer revision")
	}
	for _, other := range []domain.Destination{{Server: d.Server, ServerID: d.ServerID, AccountID: 2}, {Server: d.Server, ServerID: "replacement", AccountID: 1}, {Server: "https://other.invalid", ServerID: d.ServerID, AccountID: 1}} {
		foreign, _ := sc.PendingDeliveries(ctx, other)
		if len(foreign) != 0 {
			t.Fatal("cross destination disclosure")
		}
		wrong := newer[0]
		wrong.Destination = other
		if e := sc.CompleteDelivery(ctx, wrong, false); e != nil {
			t.Fatal(e)
		}
	}
	if e := sc.CompleteDelivery(ctx, newer[0], true); e != nil {
		t.Fatal(e)
	}
	state, e := sc.DeliveryStatus(ctx)
	if e != nil || state.Rejected != 1 {
		t.Fatal(state, e)
	}
}
func TestTrainingAndHeldIntent(t *testing.T) {
	ctx, s, sc, d, m := deliveryFixture(t)
	d.AccountID = 0
	if e := sc.RecordResult(ctx, m, &d); e != nil {
		t.Fatal(e)
	}
	tr := &Training{StartedAt: m.StartedAt, EndedAt: m.EndedAt, Source: "game"}
	if e := sc.RecordTraining(ctx, tr, &d); e != nil {
		t.Fatal(e)
	}
	state, e := sc.DeliveryStatus(ctx)
	if e != nil || state.Held != 2 {
		t.Fatal(state, e)
	}
	if _, e := sc.PendingDeliveries(ctx, d); e == nil {
		t.Fatal("unbound destination accepted")
	}
	if _, e := s.InstallationID(ctx); e != nil {
		t.Fatal(e)
	}
}
func TestPreserveLegacyRetainsRejectedAndIsIdempotent(t *testing.T) {
	ctx, s, sc, _, _ := deliveryFixture(t)
	root := t.TempDir()
	for _, name := range []string{"pending.json", "rejected/rejected.json", "outbox-old/aside.json"} {
		p := filepath.Join(root, name)
		if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(p, []byte(`{"key":"old","match":{}}`), 0600); e != nil {
			t.Fatal(e)
		}
	}
	for range 2 {
		if e := s.PreserveLegacyOutbox(ctx, root); e != nil {
			t.Fatal(e)
		}
	}
	v, e := sc.DeliveryStatus(ctx)
	if e != nil || v.LegacyHeld != 3 || v.Pending != 0 {
		t.Fatal(v, e)
	}
	if _, e := os.Stat(filepath.Join(root, "pending.json")); e != nil {
		t.Fatal("legacy source removed", e)
	}
}
func TestUpgradeV7AndReopenDelivery(t *testing.T) {
	ctx, s, sc, d, m := deliveryFixture(t)
	// Reconstruct the immediately preceding schema without touching prior migrations.
	if _, e := s.db.Exec(`DROP TABLE delivery_intents; DROP TABLE legacy_deliveries; DROP TABLE record_origins; PRAGMA user_version=7`); e != nil {
		t.Fatal(e)
	}
	if e := s.migrate(); e != nil {
		t.Fatal(e)
	}
	if e := sc.RecordResult(ctx, m, &d); e != nil {
		t.Fatal(e)
	}
	id, e := s.InstallationID(ctx)
	if e != nil {
		t.Fatal(e)
	}
	var path string
	if e := s.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path); e != nil {
		t.Fatal(e)
	}
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	restored, e := reopened.InstallationID(ctx)
	if e != nil || restored != id {
		t.Fatal("installation changed", restored, e)
	}
	pending, e := reopened.User(0).PendingDeliveries(ctx, d)
	if e != nil || len(pending) != 1 || pending[0].Match.GUID != m.GUID {
		t.Fatal("intent lost on reopen", e)
	}
}

func TestTrainingOriginsSurviveAcknowledgementAndAccountSwitch(t *testing.T) {
	for _, ack := range []bool{false, true} {
		t.Run(fmt.Sprint(ack), func(t *testing.T) {
			ctx, _, sc, a, m := deliveryFixture(t)
			b := a
			b.AccountID = 2
			first := &Training{StartedAt: m.StartedAt, EndedAt: m.StartedAt.Add(10 * time.Minute), Source: "game", Profile: "profile-a", Touches: 10}
			if err := sc.RecordTraining(ctx, first, &a); err != nil {
				t.Fatal(err)
			}
			if ack {
				p, _ := sc.PendingDeliveries(ctx, a)
				if err := sc.CompleteDelivery(ctx, p[0], false); err != nil {
					t.Fatal(err)
				}
			}
			second := &Training{StartedAt: m.StartedAt.Add(11 * time.Minute), EndedAt: m.StartedAt.Add(12 * time.Minute), Source: "game", Profile: "profile-b", Touches: 1}
			if err := sc.RecordTraining(ctx, second, &b); err != nil {
				t.Fatal(err)
			}
			p, err := sc.PendingDeliveries(ctx, b)
			if err != nil || len(p) != 1 {
				t.Fatal(p, err)
			}
			got := p[0].Training
			if got.Profile != "profile-b" || got.Touches != 1 || !got.StartedAt.Equal(second.StartedAt) || got.ID == first.ID {
				t.Fatalf("A incorporated into B: %+v", got)
			}
			// Same profile with another destination must remain isolated as well.
			third := &Training{StartedAt: second.EndedAt.Add(time.Minute), EndedAt: second.EndedAt.Add(2 * time.Minute), Source: "game", Profile: "profile-b", Touches: 2}
			if err := sc.RecordTraining(ctx, third, &a); err != nil {
				t.Fatal(err)
			}
			if third.ID == second.ID {
				t.Fatal("destination boundary depended on profile mismatch")
			}
		})
	}
}
