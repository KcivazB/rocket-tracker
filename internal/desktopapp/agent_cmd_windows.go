package desktopapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"rocket-tracker/internal/agent"
	"rocket-tracker/internal/buildinfo"
	"rocket-tracker/internal/config"
	"rocket-tracker/internal/i18n"
	"rocket-tracker/internal/setup"
	"rocket-tracker/internal/store"
	"rocket-tracker/internal/winutil"
)

// cmdAgent: `rltracker agent [run|setup|import]`.
func cmdAgent(args []string, console bool) error {
	sub := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = strings.ToLower(args[0]), args[1:]
	}
	switch sub {
	case "run":
		// The tracker sends the matches to the server of agent.json itself.
		return cmdRun(args, console, false)
	case "setup":
		return cmdAgentSetup(args, console)
	case "import":
		return cmdAgentImport(args)
	}
	return fmt.Errorf("unknown agent command %q (run, setup, import)", sub)
}

func agentConfigPath(dataDir string) string { return filepath.Join(dataDir, "agent.json") }

func newAgentClient(cfg *agent.Config) *agent.Client {
	return &agent.Client{Server: cfg.Server, Token: cfg.Token, UserAgent: "rltracker-agent/" + buildinfo.Version}
}

// cachedIni checks the game ini at most once a minute (it is reported with
// every heartbeat).
func cachedIni(rlInstallDir string) func() setup.IniStatus {
	var (
		mu sync.Mutex
		at time.Time
		st setup.IniStatus
	)
	return func() setup.IniStatus {
		mu.Lock()
		defer mu.Unlock()
		if time.Since(at) > time.Minute {
			st, at = setup.CheckIni(setup.FindInstallDir(rlInstallDir)), time.Now()
		}
		return st
	}
}

func cmdAgentSetup(args []string, console bool) error {
	var g globals
	fs := newFlagSet("agent setup")
	g.register(fs)
	serverURL := fs.String("server", "", "Rocket Tracker server URL, e.g. https://rl.example.com (required)")
	token := fs.String("token", "", "device token created in the dashboard (Appareils) (required)")
	rlDir := fs.String("rl-dir", "", "Rocket League install dir (overrides detection)")
	rate := fs.Float64("rate", setup.DefaultPacketSendRate, "PacketSendRate to write (1-120)")
	noAutostart := fs.Bool("no-autostart", false, "do not register autostart")
	noOpen := fs.Bool("no-open", false, "do not start the agent / open the dashboard")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := checkRLDir(*rlDir); err != nil {
		return err
	}
	dataDir := g.resolveDataDir()
	cfgPath := agentConfigPath(dataDir)
	cfg, err := agent.LoadConfig(cfgPath)
	if err != nil {
		cfg = &agent.Config{RLPort: setup.DefaultPort}
	}
	if *serverURL != "" {
		if cfg.Server, err = agent.NormalizeServer(*serverURL); err != nil {
			return err
		}
	}
	if *token != "" {
		cfg.Token = strings.TrimSpace(*token)
	}
	if cfg.Server == "" || cfg.Token == "" {
		return errors.New(i18n.T(lang, "agent.needArgs"))
	}
	if *rlDir != "" {
		cfg.RLInstallDir = *rlDir
	}
	if g.rlPort > 0 {
		cfg.RLPort = g.rlPort
	}

	rep := &setupReport{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	hb, err := newAgentClient(cfg).Heartbeat(ctx, agent.Report{Version: buildinfo.Version})
	cancel()
	if err != nil {
		return fmt.Errorf(i18n.T(lang, "agent.unreachable"), cfg.Server, err)
	}
	cfg.Settings = hb.Settings
	cfg.ServerID, cfg.AccountID = hb.ServerID, hb.User.ID
	if err := agent.SaveConfig(cfgPath, cfg); err != nil {
		return err
	}
	rep.add(i18n.T(lang, "agent.connected"), cfg.Server, hb.User.Name, hb.Device)

	override := cfg.RLInstallDir
	if override == "" {
		if c, err := config.Load(filepath.Join(dataDir, "config.json")); err == nil {
			override = c.Get().RLInstallDir
		}
	}
	enableStatsAPI(rep, override, *rate, cfg.RLPort, i18n.Tf(lang, "setup.dirHint", cfgPath))

	if !*noAutostart {
		registerAutostart(rep, runArgs(dataDir, g))
	}
	if n, _ := countLocalMatches(dataDir); n > 0 {
		rep.add(i18n.T(lang, "agent.localMatches"), n)
	}
	if !*noOpen {
		// A tracker already running picks agent.json up by itself.
		if err := startDetached(runArgs(dataDir, g)); err != nil {
			rep.add(i18n.T(lang, "agent.startFailed"), err)
		}
		_ = winutil.OpenURL(cfg.Server)
	}
	rep.add(i18n.T(lang, "setup.dashboard"), cfg.Server)
	if !console {
		winutil.MessageBox(i18n.T(lang, "agent.title"), strings.Join(rep.lines, "\n"), rep.warn)
	}
	return nil
}

func countLocalMatches(dataDir string) (int, error) {
	p := filepath.Join(dataDir, "rltracker.db")
	if !fileExists(p) {
		return 0, nil
	}
	st, err := store.Open(p)
	if err != nil {
		return 0, err
	}
	defer st.Close()
	return st.Count(context.Background())
}

func cmdAgentImport(args []string) error {
	var g globals
	fs := newFlagSet("agent import")
	g.register(fs)
	db := fs.String("db", "", `local database to upload (default <data dir>\rltracker.db)`)
	if err := fs.Parse(args); err != nil {
		return err
	}
	dataDir := g.resolveDataDir()
	cfg, err := agent.LoadConfig(agentConfigPath(dataDir))
	if err != nil {
		return err
	}
	path := *db
	if path == "" {
		path = filepath.Join(dataDir, "rltracker.db")
	}
	if !fileExists(path) {
		return fmt.Errorf(i18n.T(lang, "import.notFound"), path)
	}
	st, err := store.Open(path)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Printf(i18n.T(lang, "import.uploading"), path, cfg.Server)
	n, man, err := agent.Import(ctx, newAgentClient(cfg), st, func(done, total int) {
		if done%25 == 0 || done == total {
			fmt.Printf(i18n.T(lang, "import.progress"), done, total)
		}
	})
	if err != nil {
		return fmt.Errorf(i18n.T(lang, "import.stopped"), n, err)
	}
	fmt.Printf(i18n.T(lang, "import.done"), n, man)
	return nil
}
