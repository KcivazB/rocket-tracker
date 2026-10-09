// Command rltracker records Rocket League matches from the official Stats API
// and serves a local dashboard. See docs/SPEC.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"rocket-tracker/internal/agent"
	"rocket-tracker/internal/config"
	"rocket-tracker/internal/i18n"
	"rocket-tracker/internal/logging"
	"rocket-tracker/internal/server"
	"rocket-tracker/internal/setup"
	"rocket-tracker/internal/sim"
	"rocket-tracker/internal/statsapi"
	"rocket-tracker/internal/store"
	"rocket-tracker/internal/tilt"
	"rocket-tracker/internal/tracker"
	"rocket-tracker/internal/winutil"
	"rocket-tracker/web"
)

// Version is the application version (overridable with -ldflags -X main.Version=...).
var Version = "0.10.0"

type globals struct {
	dataDir string
	port    int
	rlPort  int
	debug   bool
}

func (g *globals) register(fs *flag.FlagSet) {
	fs.StringVar(&g.dataDir, "data-dir", "", `data directory (default %APPDATA%\RocketTracker)`)
	fs.IntVar(&g.port, "port", 0, "dashboard port (default from config, 8765)")
	fs.IntVar(&g.rlPort, "rl-port", 0, "Rocket League Stats API port (default from config, 49123)")
	fs.BoolVar(&g.debug, "debug", false, "verbose logging")
}

func (g *globals) resolveDataDir() string {
	if g.dataDir != "" {
		if abs, err := filepath.Abs(g.dataDir); err == nil {
			return abs
		}
		return g.dataDir
	}
	base, err := os.UserConfigDir() // %APPDATA% on Windows
	if err != nil || base == "" {
		base = "."
	}
	return filepath.Join(base, "RocketTracker")
}

const usage = `Rocket Tracker %s — Rocket League match tracker

Usage: rltracker [command] [flags]

Commands:
  run         (default) track matches and serve the dashboard
  setup       enable the Stats API in the game ini, register autostart, open the dashboard
  agent       track matches and send them to a Rocket Tracker server (instead of run)
  agent setup --server URL --token TOKEN   connect this PC to the server, enable the Stats API, autostart the agent
  agent import [--db PATH]                 upload the matches of the local database to the server
  uninstall   remove autostart
  open        open the dashboard in the browser
  simulate    fake Stats API server   [--port 49123] [--mode ws|tcp] [--matches N] [--speed X]
  seed        insert fake matches     --db path [--n 300] [--days 90]
  version     print version

Global flags: --data-dir DIR  --port 8765  --rl-port 49123  --debug
`

// lang is the language of the messages shown to the player: $RT_LANG, else
// the Windows display language (French or English).
var lang = i18n.EN

func main() {
	console := winutil.AttachParentConsole()
	if l, ok := i18n.FromEnv(); ok {
		lang = l
	} else if winutil.UILanguageIsFrench() {
		lang = i18n.FR
	}
	args := os.Args[1:]
	cmd := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = strings.ToLower(args[0]), args[1:]
	}
	var err error
	switch cmd {
	case "run":
		err = cmdRun(args, console)
	case "setup":
		err = cmdSetup(args, console)
	case "agent":
		err = cmdAgent(args, console)
	case "uninstall":
		err = cmdUninstall(args, console)
	case "open":
		err = cmdOpen(args)
	case "simulate", "sim":
		err = cmdSimulate(args)
	case "seed":
		err = cmdSeed(args)
	case "version", "--version", "-v":
		fmt.Println(Version)
	case "help", "-h", "--help", "/?":
		fmt.Printf(usage, Version)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		fmt.Fprintf(os.Stderr, usage, Version)
		os.Exit(2)
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		if !console && cmd != "run" {
			winutil.MessageBox("Rocket Tracker", i18n.Tf(lang, "error.box", err.Error()), true)
		}
		os.Exit(1)
	}
	restartIfUpdated()
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

func shortHash(s string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(s)))
	return strconv.FormatUint(uint64(h.Sum32()), 16)
}

// ---------------------------------------------------------------- run

func cmdRun(args []string, console bool) error {
	var g globals
	fs := newFlagSet("run")
	g.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	dataDir := g.resolveDataDir()
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}

	log, logFile, err := openLog(dataDir, "rltracker.log", g.debug, console)
	if err != nil {
		return err
	}
	defer logFile.Close()

	release, ok, err := winutil.SingleInstance("RocketTracker-" + shortHash(dataDir))
	if err != nil {
		log.Warn("single-instance check failed", "err", err)
	} else if !ok {
		log.Info("another instance is already running; exiting", "data_dir", dataDir)
		return nil
	}
	defer release()

	cfgMgr, err := config.Load(filepath.Join(dataDir, "config.json"))
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	cfg := cfgMgr.Get()
	port := cfg.DashboardPort
	if g.port > 0 {
		port = g.port
	}
	rlPort := cfg.RLPort
	if g.rlPort > 0 {
		rlPort = g.rlPort
	}

	st, err := store.Open(filepath.Join(dataDir, "rltracker.db"))
	if err != nil {
		log.Error("cannot open database", "err", err)
		return err
	}
	defer st.Close()
	// Merges a WAL left behind by a killed run (or an older version).
	if err := st.Checkpoint(context.Background()); err != nil {
		log.Warn("database checkpoint failed", "err", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var desk *desktop // set below, before the game client can save matches
	tr := tracker.New(tracker.Options{
		Log: log.With("component", "tracker"),
		Identity: func() tracker.Identity {
			c := cfgMgr.Get()
			return tracker.Identity{Names: c.PlayerNames, IDs: c.PlayerIDs}
		},
		OnAutoIdentity: func(name, id string) {
			if stored, err := cfgMgr.AddAutoIdentity(name, id); err != nil {
				log.Warn("could not persist auto-detected identity", "err", err)
			} else if stored {
				log.Info("auto-detected player identity saved to config", "name", name, "primary_id", id)
			}
		},
		DefaultTag: func() string { return cfgMgr.Get().DefaultTag },
		Save: func(m *store.Match) error {
			sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := st.Save(sctx, m); err != nil {
				return err
			}
			// The app is killed rather than closed: keep the .db file complete.
			if err := st.Checkpoint(sctx); err != nil {
				log.Warn("database checkpoint failed", "err", err)
			}
			if desk != nil {
				go func() {
					ms, err := st.List(context.Background())
					if err != nil {
						log.Warn("tilt check: cannot list matches", "err", err)
						return
					}
					desk.Tilt(tilt.Check(ms, cfgMgr.Get().TiltStreak, time.Now()))
				}()
			}
			return nil
		},
	})

	client := gameClient(log, tr, cfg.RLInstallDir, rlPort, g.rlPort > 0)

	desk = startDesktop(ctx, stop, log, fmt.Sprintf("http://localhost:%d", port))
	defer desk.Stop()

	srv := &server.Server{
		Version: Version, Store: st, Config: cfgMgr, Tracker: tr, ConnStatus: client.Status,
		Static: web.FS, Log: log.With("component", "http"),
		Update: desk.Newer, InstallUpdate: desk.Install, Restart: desk.Restart,
	}

	waitClient := runGameClient(ctx, stop, log, client)

	log.Info("rocket tracker started", "version", Version, "data_dir", dataDir, "dashboard", fmt.Sprintf("http://localhost:%d", port), "rl_port", client.Port)
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe(ctx, port) }()
	select {
	case err = <-errc:
		if err != nil {
			log.Error("http server failed", "port", port, "err", err)
			waitClient()
			tr.Flush()
			return err
		}
	case <-ctx.Done():
		<-errc
	}
	waitClient()
	tr.Flush()
	log.Info("rocket tracker stopped")
	return nil
}

// gameClient builds the Stats API client feeding tr. The ports written in the
// game ini win over the configured ones, unless the port was forced by flag.
func gameClient(log *slog.Logger, tr *tracker.Tracker, rlInstallDir string, rlPort int, portForced bool) *statsapi.Client {
	client := &statsapi.Client{
		Port:         rlPort,
		WebPort:      setup.DefaultWebPort,
		Log:          log.With("component", "statsapi"),
		OnEvent:      tr.HandleEvent,
		OnDisconnect: tr.Disconnected,
	}
	if ini := setup.CheckIni(setup.FindInstallDir(rlInstallDir)); ini.Found {
		log.Info("stats api ini", "path", ini.Path, "packet_send_rate", ini.PacketSendRate, "ok", ini.OK)
		if ini.WebPort > 0 {
			client.WebPort = ini.WebPort
		}
		if !portForced && ini.Port > 0 && ini.Port != rlPort {
			log.Info("using Port from ini", "port", ini.Port)
			client.Port = ini.Port
		}
	} else {
		log.Warn("Stats API ini not found; run `rltracker setup`", "install_dir", ini.InstallDir)
	}
	return client
}

// runGameClient runs the client in the background. The returned function
// cancels the context and waits for the client to stop: its disconnect
// callback saves the current match, so call it before flushing the tracker.
func runGameClient(ctx context.Context, stop context.CancelFunc, log *slog.Logger, client *statsapi.Client) (wait func()) {
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		defer func() {
			if r := recover(); r != nil {
				log.Error("stats api client crashed", "panic", fmt.Sprint(r))
			}
		}()
		client.Run(ctx)
	}()
	return func() {
		stop()
		select {
		case <-clientDone:
		case <-time.After(5 * time.Second):
			log.Warn("stats api client did not stop in time")
		}
	}
}

// openLog opens the rotating log file of a command.
func openLog(dataDir, name string, debug, console bool) (*slog.Logger, io.Closer, error) {
	logFile, err := logging.OpenRotating(filepath.Join(dataDir, name), logging.DefaultMaxSize, 2)
	if err != nil {
		return nil, nil, fmt.Errorf("open log: %w", err)
	}
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	var stderr io.Writer
	if console {
		stderr = os.Stderr
	}
	return logging.New(level, logFile, stderr), logFile, nil
}

// ---------------------------------------------------------------- setup

type setupReport struct {
	lines []string
	warn  bool
}

func (r *setupReport) add(format string, a ...any) {
	l := fmt.Sprintf(format, a...)
	r.lines = append(r.lines, l)
	fmt.Println(l)
}

func cmdSetup(args []string, console bool) error {
	var g globals
	fs := newFlagSet("setup")
	g.register(fs)
	iniOnly := fs.Bool("ini-only", false, "only patch the ini (used by the elevated child)")
	rlDir := fs.String("rl-dir", "", "Rocket League install dir (overrides detection)")
	rate := fs.Float64("rate", setup.DefaultPacketSendRate, "PacketSendRate to write (1-120)")
	noAutostart := fs.Bool("no-autostart", false, "do not register autostart")
	noOpen := fs.Bool("no-open", false, "do not start the tracker / open the dashboard")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dataDir := g.resolveDataDir()
	rlPort := g.rlPort
	if rlPort <= 0 {
		rlPort = setup.DefaultPort
	}

	if *iniOnly {
		if *rlDir == "" {
			return errors.New("--ini-only requires --rl-dir")
		}
		_, err := setup.EnableStatsAPI(*rlDir, *rate, rlPort, setup.DefaultWebPort)
		return err
	}
	if err := checkRLDir(*rlDir); err != nil {
		return err
	}

	rep := &setupReport{}
	cfgMgr, err := config.Load(filepath.Join(dataDir, "config.json"))
	if err != nil {
		return err
	}
	cfg := cfgMgr.Get()
	override := *rlDir
	if override == "" {
		override = cfg.RLInstallDir
	}
	enableStatsAPI(rep, override, *rate, rlPort, i18n.Tf(lang, "setup.dirHint", cfgMgr.Path()))

	if !*noAutostart {
		registerAutostart(rep, runArgs(dataDir, g))
	}

	port := cfg.DashboardPort
	if g.port > 0 {
		port = g.port
	}
	url := fmt.Sprintf("http://localhost:%d", port)
	if !*noOpen {
		if !dashboardUp(port) {
			if err := startDetached(runArgs(dataDir, g)); err != nil {
				rep.add(i18n.T(lang, "setup.startFailed"), err)
			} else {
				for i := 0; i < 20 && !dashboardUp(port); i++ {
					time.Sleep(250 * time.Millisecond)
				}
			}
		}
		_ = winutil.OpenURL(url)
	}
	rep.add(i18n.T(lang, "setup.dashboard"), url)

	if !console {
		winutil.MessageBox(i18n.T(lang, "setup.title"), strings.Join(rep.lines, "\n"), rep.warn)
	}
	return nil
}

// checkRLDir rejects an explicit --rl-dir that is not a Rocket League
// install (detection would otherwise silently patch another one).
func checkRLDir(dir string) error {
	if dir != "" && !setup.IsInstallDir(dir) {
		return fmt.Errorf("--rl-dir %q is not a Rocket League install (expected Binaries\\Win64\\RocketLeague.exe or TAGame\\Config)", dir)
	}
	return nil
}

// enableStatsAPI finds Rocket League and turns the Stats API on in its ini
// (asking for elevation when Program Files is not writable).
func enableStatsAPI(rep *setupReport, override string, rate float64, rlPort int, dirHint string) {
	dir := setup.FindInstallDir(override)
	if dir == "" {
		rep.warn = true
		rep.add(i18n.T(lang, "setup.notFound"), dirHint)
	} else {
		rep.add(i18n.T(lang, "setup.found"), dir)
		_, err := setup.EnableStatsAPI(dir, rate, rlPort, setup.DefaultWebPort)
		if err != nil && setup.IsPermission(err) && !winutil.IsElevated() {
			rep.add("%s", i18n.T(lang, "setup.elevate"))
			exe, _ := os.Executable()
			code, eerr := winutil.RunElevated(exe, []string{"setup", "--ini-only", "--rl-dir", dir,
				"--rate", strconv.FormatFloat(rate, 'f', -1, 64), "--rl-port", strconv.Itoa(rlPort)})
			switch {
			case eerr != nil:
				err = eerr
			case code != 0:
				err = fmt.Errorf("elevated setup exited with code %d", code)
			default:
				err = nil
			}
		}
		st := setup.CheckIni(dir)
		if err != nil {
			rep.warn = true
			rep.add(i18n.T(lang, "setup.enableFailed"), err)
		} else if st.OK {
			rep.add(i18n.T(lang, "setup.enabled"), st.Path, st.PacketSendRate, st.Port)
			rep.add("%s", i18n.T(lang, "setup.restart"))
		} else {
			rep.warn = true
			rep.add(i18n.T(lang, "setup.checkFailed"), st.Path)
		}
	}
}

// registerAutostart starts this exe with args at Windows login (one entry:
// the local tracker and the agent replace each other).
func registerAutostart(rep *setupReport, args []string) {
	exe, err := os.Executable()
	if err == nil {
		if err = winutil.SetAutostart(commandLine(exe, args)); err == nil {
			rep.add(i18n.T(lang, "setup.autostart"), winutil.AutostartName)
		}
	}
	if err != nil {
		rep.warn = true
		rep.add(i18n.T(lang, "setup.autostartFailed"), err)
	}
}

func dashboardUp(port int) bool {
	c := http.Client{Timeout: 700 * time.Millisecond}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/api/status", port))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// runArgs are the arguments of the background "run" process started by setup
// (autostart and immediate start use the same ones).
func runArgs(dataDir string, g globals) []string {
	args := []string{"run"}
	if g.dataDir != "" {
		args = append(args, "--data-dir", dataDir)
	}
	if g.port > 0 {
		args = append(args, "--port", strconv.Itoa(g.port))
	}
	return args
}

// commandLine builds a Windows command line with proper quoting (spaces,
// embedded quotes, trailing backslashes such as `D:\`).
func commandLine(exe string, args []string) string {
	// Always quote the program path: an unquoted path with spaces is
	// ambiguous for CreateProcess.
	parts := []string{`"` + exe + `"`}
	for _, a := range args {
		parts = append(parts, syscall.EscapeArg(a))
	}
	return strings.Join(parts, " ")
}

// startDetached starts this exe with args in the background.
func startDetached(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000008 | 0x00000200} // DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// ---------------------------------------------------------------- misc commands

func cmdUninstall(args []string, console bool) error {
	var g globals
	fs := newFlagSet("uninstall")
	g.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := winutil.RemoveAutostart(); err != nil {
		return err
	}
	msg := i18n.Tf(lang, "uninstall.done", g.resolveDataDir())
	fmt.Println(msg)
	if !console {
		winutil.MessageBox("Rocket Tracker", msg, false)
	}
	return nil
}

func cmdOpen(args []string) error {
	var g globals
	fs := newFlagSet("open")
	g.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if g.port <= 0 {
		if cfg, err := agent.LoadConfig(agentConfigPath(g.resolveDataDir())); err == nil {
			fmt.Println(cfg.Server)
			return winutil.OpenURL(cfg.Server)
		}
	}
	port := g.port
	if port <= 0 {
		port = 8765
		if p := filepath.Join(g.resolveDataDir(), "config.json"); fileExists(p) {
			if m, err := config.Load(p); err == nil {
				port = m.Get().DashboardPort
			}
		}
	}
	url := fmt.Sprintf("http://localhost:%d", port)
	fmt.Println(url)
	return winutil.OpenURL(url)
}

func cmdSimulate(args []string) error {
	fs := newFlagSet("simulate")
	port := fs.Int("port", 49123, "port to listen on")
	mode := fs.String("mode", "tcp", "transport: ws or tcp")
	matches := fs.Int("matches", 5, "number of matches")
	speed := fs.Float64("speed", 1, "game-time speed multiplier")
	seed := fs.Uint64("seed", 0, "random seed (0 = random)")
	name := fs.String("name", "SimPlayer", "simulated player name")
	kinds := fs.String("kinds", "", "comma-separated match kinds to cycle: normal,forfeit,abandoned,offline (default random)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *mode != "ws" && *mode != "tcp" {
		return fmt.Errorf("--mode must be ws or tcp")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return sim.Run(ctx, sim.Options{Port: *port, Mode: *mode, Matches: *matches, Speed: *speed, Seed: *seed, MeName: *name, Out: os.Stdout, Kinds: splitList(*kinds)})
}

func cmdSeed(args []string) error {
	fs := newFlagSet("seed")
	n := fs.Int("n", 300, "number of matches")
	db := fs.String("db", "", "target database path (required)")
	days := fs.Int("days", 90, "spread over the last N days")
	seed := fs.Uint64("seed", 0, "random seed (0 = random)")
	name := fs.String("name", "SimPlayer", "player name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *db == "" {
		return errors.New("seed requires --db <path> (it never writes the real database implicitly)")
	}
	st, err := store.Open(*db)
	if err != nil {
		return err
	}
	defer st.Close()
	ms := sim.Generate(sim.SeedOptions{N: *n, Days: *days, Seed: *seed, MeName: *name})
	if err := st.SaveMany(context.Background(), ms); err != nil {
		return err
	}
	total, _ := st.Count(context.Background())
	fmt.Printf("inserted %d matches into %s (total %d)\n", len(ms), *db, total)
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
