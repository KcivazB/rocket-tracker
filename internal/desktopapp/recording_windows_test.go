package desktopapp

import (
	"context"
	"path/filepath"
	"testing"

	"rocket-tracker/internal/agent"
	"rocket-tracker/internal/domain"
)

// Native composition regression: persisted setup B wins even while the old
// uploader A is still alive and has not reloaded its in-memory configuration.
func TestRecordingIgnoresStaleUploaderAfterSetup(t *testing.T) {
	path, sc, a, m := recordingFixture(t)
	up := &uplink{dataDir: filepath.Dir(path), records: sc, a: agent.New(a, agent.Options{})}
	b := &agent.Config{Server: a.Server, Token: "synthetic-b", ServerID: a.ServerID, AccountID: 2}
	if err := agent.SaveConfig(path, b); err != nil {
		t.Fatal(err)
	}
	if err := configuredRecorder(sc, path).RecordResult(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	selected, err := up.Destination()
	if err != nil || selected.AccountID != 2 {
		t.Fatal(selected, err)
	}
	pending, err := sc.PendingDeliveries(context.Background(), domain.Destination{Server: b.Server, ServerID: b.ServerID, AccountID: b.AccountID})
	if err != nil || len(pending) != 1 {
		t.Fatal(pending, err)
	}
	old, err := sc.PendingDeliveries(context.Background(), aDestination(a))
	if err != nil || len(old) != 0 {
		t.Fatal(old, err)
	}
}
func aDestination(c *agent.Config) domain.Destination {
	return domain.Destination{Server: c.Server, ServerID: c.ServerID, AccountID: c.AccountID}
}
