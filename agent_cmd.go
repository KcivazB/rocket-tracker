package main

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
	"rocket-tracker/internal/config"
	"rocket-tracker/internal/i18n"
	"rocket-tracker/internal/setup"
	"rocket-tracker/internal/store"
	"rocket-tracker/internal/tracker"
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
		return cmdAgentRun(args, console)
	case "setup":
		return cmdAgentSetup(args, console)
	case "import":
		return cmdAgentImport(args)
	}
	return fmt.Errorf("unknown agent command %q (run, setup, import)", sub)
}

func agentConfigPath(dataDir string) string { return filepath.Join(dataDir, "agent.json") }

// agentArgs are the arguments of the autostarted agent.
func agentArgs(dataDir string, g globals) []string {
	args := []string{"agent"}
	if g.dataDir != "" {
		args = append(args, "--data-dir", dataDir)
	}
	return args
}

func newAgentClient(cfg *agent.Config) *agent.Client {
	return &agent.Client{Server: cfg.Server, Token: cfg.Token, UserAgent: "rltracker-agent/" + Version}
}

func cmdAgentRun(args []string, console bool) error {
	var g globals
	fs := newFlagSet("agent")
	g.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	dataDir := g.resolveDataDir()
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	log, logFile, err := openLog(dataDir, "agent.log", g.debug, console)
	if err != nil {
		return err
	}
	defer logFile.Close()

	release, ok, err := winutil.SingleInstance("RocketTracker-agent-" + shortHash(dataDir))
	if err != nil {
		log.Warn("single-instance check failed", "err", err)
	} else if !ok {
		log.Info("another agent is already running; exiting", "data_dir", dataDir)
		return nil
	}
	defer release()

	cfgPath := agentConfigPath(dataDir)
	cfg, err := agent.LoadConfig(cfgPath)
	if err != nil {
		log.Error("cannot start the agent", "err", err)
		return err
	}
	rlPort := cfg.RLPort
	if g.rlPort > 0 {
		rlPort = g.rlPort
	}
	outbox, err := agent.OpenOutbox(filepath.Join(dataDir, "outbox"))
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a := agent.New(cfg, agent.Options{
		Client: newAgentClient(cfg), Outbox: outbox, Version: Version, ConfigPath: cfgPath,
		Log: log.With("component", "agent"), Ini: cachedIni(cfg.RLInstallDir),
	})
	tr := tracker.New(tracker.Options{
		Log:            log.With("component", "tracker"),
		Identity:       a.Identity,
		OnAutoIdentity: a.OnAutoIdentity,
		DefaultTag:     a.DefaultTag,
		Save:           a.Save,
	})
	a.Tracker = tr
	client := gameClient(log, tr, cfg.RLInstallDir, rlPort, g.rlPort > 0)
	a.ConnStatus = client.Status
	waitClient := runGameClient(ctx, stop, log, client)
	desk := startDesktop(ctx, stop, log, cfg.Server)
	defer desk.Stop()

	log.Info("rocket tracker agent started", "version", Version, "server", cfg.Server, "data_dir", dataDir, "rl_port", client.Port)
	a.Run(ctx)
	waitClient()
	tr.Flush() // queues the match in progress, if any
	fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	a.Flush(fctx)
	cancel()
	log.Info("rocket tracker agent stopped")
	return nil
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
	hb, err := newAgentClient(cfg).Heartbeat(ctx, agent.Report{Version: Version})
	cancel()
	if err != nil {
		return fmt.Errorf(i18n.T(lang, "agent.unreachable"), cfg.Server, err)
	}
	cfg.Settings = hb.Settings
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
		registerAutostart(rep, agentArgs(dataDir, g))
	}
	if dashboardUp(8765) {
		rep.warn = true
		rep.add("%s", i18n.T(lang, "agent.localRunning"))
	}
	if n, _ := countLocalMatches(dataDir); n > 0 {
		rep.add(i18n.T(lang, "agent.localMatches"), n)
	}
	if !*noOpen {
		if err := startDetached(agentArgs(dataDir, g)); err != nil {
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
