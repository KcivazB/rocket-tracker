package desktopapp

import (
	"context"
	"path/filepath"
	"rocket-tracker/internal/agent"
	"rocket-tracker/internal/application"
	"rocket-tracker/internal/store"
	"testing"
	"time"
)

func TestUplinkRecordsWhileUploaderStopped(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "local.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	up := &uplink{dataDir: dir, records: st.User(0)}
	recorder := application.Recorder{Records: st.User(0), Destination: up.Destination}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m := &store.Match{GUID: "local-only", Online: true, StartedAt: at, EndedAt: at.Add(time.Minute), Result: "win"}
	if err := recorder.RecordResult(ctx, m); err != nil {
		t.Fatal(err)
	}
	status, err := st.User(0).DeliveryStatus(ctx)
	if err != nil || status.Pending != 0 || status.Held != 0 {
		t.Fatal(status, err)
	}
	cfg := &agent.Config{Server: "https://destination.invalid", Token: "synthetic", ServerID: "known-server", AccountID: 7}
	if err := agent.SaveConfig(agentConfigPath(dir), cfg); err != nil {
		t.Fatal(err)
	}
	m.GUID = "configured-offline"
	if err := recorder.RecordResult(ctx, m); err != nil {
		t.Fatal(err)
	}
	status, err = st.User(0).DeliveryStatus(ctx)
	if err != nil || status.Pending != 1 {
		t.Fatal(status, err)
	}
	cfg.AccountID = 0
	if err := agent.SaveConfig(agentConfigPath(dir), cfg); err != nil {
		t.Fatal(err)
	}
	m.GUID = "unverified"
	if err := recorder.RecordResult(ctx, m); err != nil {
		t.Fatal(err)
	}
	status, err = st.User(0).DeliveryStatus(ctx)
	if err != nil || status.Held != 1 {
		t.Fatal(status, err)
	}
}
