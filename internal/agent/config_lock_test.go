package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// The same test binary exercises the OS lock from a genuinely separate process.
func TestConfigLockProcessHelper(t *testing.T) {
	mode := os.Getenv("ROCKET_TEST_CONFIG_LOCK_MODE")
	if mode == "" {
		return
	}
	path := os.Getenv("ROCKET_TEST_CONFIG_LOCK_PATH")
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	err = lockConfigFile(f, mode == "probe")
	if mode == "probe" {
		if err == nil {
			unlockConfigFile(f)
			t.Fatal("other process acquired the config lock inside CAS")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("locked")
	// Parent holds stdin open and kills the helper to test crash release.
	_, _ = bufio.NewReader(os.Stdin).ReadByte()
	unlockConfigFile(f)
}

func configHelper(ctx context.Context, path, mode string) *exec.Cmd {
	c := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestConfigLockProcessHelper$")
	c.Env = append(os.Environ(), "ROCKET_TEST_CONFIG_LOCK_MODE="+mode, "ROCKET_TEST_CONFIG_LOCK_PATH="+path)
	return c
}

func TestConfigCASLocksAcrossProcessesAndPreservesGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	cfg := &Config{Server: "https://a.invalid", Token: "synthetic"}
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	first := cfg.Generation
	if first == "" {
		t.Fatal("setup has no generation")
	}
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Generation == first {
		t.Fatal("same-target setup reused its generation")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	probeErr := error(nil)
	_, err := updateConfig(path, cfg, func(c *Config) {
		// updateConfig has compared generation+target and has not renamed yet.
		// A separate process must still be excluded during this exact interval.
		out, e := configHelper(ctx, path, "probe").CombinedOutput()
		if e != nil {
			probeErr = fmt.Errorf("lock probe: %w: %s", e, out)
		}
		c.Settings.DefaultTag = "casual"
	})
	if err != nil {
		t.Fatal(err)
	}
	if probeErr != nil {
		t.Fatal(probeErr)
	}
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Generation != cfg.Generation || got.Settings.DefaultTag != "casual" {
		t.Fatal("background patch changed the setup generation or lost settings")
	}
}

func TestConfigLockReleasedOnProcessTermination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := configHelper(ctx, path, "hold")
	stdout, err := c.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := c.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Process.Kill() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "locked\n" {
		_ = c.Process.Kill()
		_ = c.Wait()
		t.Fatal("child did not acquire lock", err)
	}
	if err := c.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = c.Wait()
	done := make(chan struct{})
	var saveErr error
	go func() {
		saveErr = SaveConfig(path, &Config{Server: "https://b.invalid", Token: "synthetic"})
		close(done)
	}()
	awaitConfigTest(t, done)
	if saveErr != nil {
		t.Fatal(saveErr)
	}
	if _, err := os.Stat(path + ".lock"); err != nil {
		t.Fatal("persistent companion lock was removed", err)
	}
}

func TestConfigCASRejectsTargetChangeEvenWithSameGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	a := &Config{Server: "https://a.invalid", Token: "synthetic-a"}
	if err := SaveConfig(path, a); err != nil {
		t.Fatal(err)
	}
	b := cloneConfig(a)
	b.Server, b.Token = "https://b.invalid", "synthetic-b"
	if err := withConfigLock(path, func() error { return writeConfig(path, b) }); err != nil {
		t.Fatal(err)
	}
	called := false
	_, err := updateConfig(path, a, func(*Config) { called = true })
	if !errors.Is(err, ErrConfigChanged) || called {
		t.Fatal("target change bypassed CAS")
	}
	got, err := LoadConfig(path)
	if err != nil || !reflect.DeepEqual(got, b) {
		t.Fatal("rejected update altered current configuration")
	}
}

func TestConfigRemovedSetupCannotBeResurrected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	cfg := &Config{Server: "https://a.invalid", Token: "synthetic"}
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	if err := RemoveConfig(path); err != nil {
		t.Fatal(err)
	}
	_, err := updateConfig(path, cfg, func(c *Config) { c.Settings.DefaultTag = "casual" })
	if !errors.Is(err, ErrConfigChanged) {
		t.Fatal("background update did not reject deleted setup")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("background update recreated agent.json")
	}
}

func TestConfigLegacyGenerationAndUnchangedHeartbeat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	cfg := &Config{Server: "https://a.invalid", Token: "synthetic"}
	if err := withConfigLock(path, func() error { return writeConfig(path, cfg) }); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil || loaded.Generation != "" {
		t.Fatal("legacy configuration failed to load")
	}
	_, err = updateConfig(path, loaded, func(c *Config) { c.Settings.DefaultTag = "casual" })
	if err != nil {
		t.Fatal(err)
	}
	loaded, err = LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Generation != "" || loaded.Settings.DefaultTag != "casual" {
		t.Fatal("legacy patch failed")
	}
	at := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := updateConfig(path, loaded, func(*Config) {}); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("unchanged background reply rewrote configuration")
	}
}
