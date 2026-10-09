package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSaveAndAutoIdentity(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "config.json")
	m, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	c := m.Get()
	if c.DefaultTag != "ranked" || c.DashboardPort != 8765 || c.RLPort != 49123 || c.PlayerNames == nil {
		t.Fatalf("defaults %+v", c)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal("config not written")
	}
	if ok, _ := m.AddAutoIdentity("Me", "Epic|1|0"); !ok {
		t.Fatal("auto identity not stored")
	}
	if ok, _ := m.AddAutoIdentity("Other", "Epic|2|0"); ok {
		t.Fatal("auto identity must not override an existing one")
	}
	m2, _ := Load(p)
	if c := m2.Get(); len(c.PlayerIDs) != 1 || c.PlayerIDs[0] != "Epic|1|0" || c.PlayerNames[0] != "Me" {
		t.Fatalf("reloaded %+v", c)
	}
	if c, _ := m2.Set(Config{DefaultTag: "BAD", DashboardPort: -1, PlayerNames: []string{"a", "A", " "}}); c.DefaultTag != "ranked" || c.DashboardPort != 8765 || len(c.PlayerNames) != 1 {
		t.Fatalf("normalize %+v", c)
	}
	// Corrupt file => defaults, backup kept.
	_ = os.WriteFile(p, []byte("{nope"), 0o644)
	m3, err := Load(p)
	if err != nil || m3.Get().DefaultTag != "ranked" {
		t.Fatalf("corrupt: %v", err)
	}
	if _, err := os.Stat(p + ".bad"); err != nil {
		t.Fatal("backup of corrupt config missing")
	}
}

func TestGoalNormalize(t *testing.T) {
	c := Defaults()
	c.Normalize()
	if c.Goal.Mode != "1v1" || c.Goal.Daily != 10 || c.Goal.SeasonStart != "" {
		t.Fatalf("default goal: %+v", c.Goal)
	}
	c.Goal = Goal{Mode: " 2V2 ", Daily: 15, SeasonStart: "2026-09-01", SeasonEnd: "2026-12-09"}
	c.Normalize()
	if c.Goal != (Goal{Mode: "2v2", Daily: 15, SeasonStart: "2026-09-01", SeasonEnd: "2026-12-09"}) {
		t.Fatalf("valid goal changed: %+v", c.Goal)
	}
	c.Goal = Goal{Mode: "5v5", Daily: 0, SeasonStart: "01/09/2026", SeasonEnd: "2026-12-09"}
	c.Normalize()
	if c.Goal.Mode != "1v1" || c.Goal.Daily != 10 || c.Goal.SeasonStart != "" || c.Goal.SeasonEnd != "2026-12-09" {
		t.Fatalf("invalid goal not fixed: %+v", c.Goal)
	}
	c.Goal = Goal{Mode: "1v1", Daily: 10, SeasonStart: "2026-12-01", SeasonEnd: "2026-09-01"}
	c.Normalize()
	if c.Goal.SeasonEnd != "" {
		t.Fatalf("end before start kept: %+v", c.Goal)
	}
}

func TestValidDiscordWebhook(t *testing.T) {
	for u, want := range map[string]bool{
		"https://discord.com/api/webhooks/123/abc":        true,
		"https://discordapp.com/api/webhooks/123/abc":     true,
		"https://ptb.discord.com/api/webhooks/123/abc":    true,
		"http://discord.com/api/webhooks/123/abc":         false, // not https
		"https://discord.com.evil.example/api/webhooks/1": false,
		"https://discord.com:8443/api/webhooks/123/abc":   false,
		"https://user@discord.com/api/webhooks/123/abc":   false,
		"https://discord.com/api/webhooks/":               false,
		"https://discord.com/channels/1/2":                false,
		"https://192.168.1.10/api/webhooks/1/x":           false,
		"":                                                false,
	} {
		if got := ValidDiscordWebhook(u); got != want {
			t.Errorf("%q: %v, want %v", u, got, want)
		}
	}
}
