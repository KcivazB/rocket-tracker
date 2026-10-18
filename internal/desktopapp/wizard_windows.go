package desktopapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"rocket-tracker/internal/agent"
	"rocket-tracker/internal/buildinfo"
	"rocket-tracker/internal/config"
	"rocket-tracker/internal/domain"
	"rocket-tracker/internal/i18n"
	"rocket-tracker/internal/server"
	"rocket-tracker/internal/setup"
	"rocket-tracker/internal/store"
	"rocket-tracker/internal/tracker"
	"rocket-tracker/internal/winutil"
)

// uplink sends the matches recorded by `run` to the server of agent.json,
// when there is one. The local database keeps every match too, so the local
// dashboard still works while the server is down.
type uplink struct {
	records *store.Scope

	ctx     context.Context
	log     *slog.Logger
	dataDir string
	rlDir   func() string // local install dir setting
	tracker *tracker.Tracker
	conn    func() (bool, string)

	mu   sync.Mutex
	a    *agent.Agent
	stop context.CancelFunc
	done chan struct{}

	obMu sync.Mutex
	ob   *agent.Outbox // opened once, shared by the successive agents
}

// agent is the running agent, or nil (local only).
func (u *uplink) agent() *agent.Agent {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.a
}

// Server is the server the matches go to ("" = local only).
func (u *uplink) Server() string {
	if a := u.agent(); a != nil {
		return a.ServerURL()
	}
	return ""
}

// Reachable reports whether the server answered the last heartbeat.
func (u *uplink) Reachable() bool {
	a := u.agent()
	return a != nil && a.Reachable()
}

// outbox returns the outbox, meant for server (see Outbox.SetDestination).
func (u *uplink) outbox(server string) (*agent.Outbox, error) {
	u.obMu.Lock()
	defer u.obMu.Unlock()
	if u.ob == nil {
		ob, err := agent.OpenOutbox(filepath.Join(u.dataDir, "outbox"))
		if err != nil {
			return nil, err
		}
		u.ob = ob
	}
	if u.records != nil {
		return u.ob, nil
	}
	if _, err := u.ob.SetDestination(server, u.log); err != nil {
		return nil, err
	}
	return u.ob, nil
}

// start sends the matches to cfg.Server from now on (replacing the agent
// already running, if any).
func (u *uplink) start(cfg *agent.Config) error {
	u.Stop()
	outbox, err := u.outbox(cfg.Server)
	if err != nil {
		return err
	}
	rlDir := cfg.RLInstallDir
	if rlDir == "" {
		rlDir = u.rlDir()
	}
	a := agent.New(cfg, agent.Options{
		Client: newAgentClient(cfg), Outbox: outbox, Journal: u.records, Version: buildinfo.Version, ConfigPath: agentConfigPath(u.dataDir),
		Log: u.log.With("component", "agent"), Ini: cachedIni(rlDir), Tracker: u.tracker, ConnStatus: u.conn,
	})
	ctx, stop := context.WithCancel(u.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.Run(ctx)
	}()
	u.mu.Lock()
	u.a, u.stop, u.done = a, stop, done
	u.mu.Unlock()
	u.log.Info("sending matches to the server", "server", cfg.Server)
	return nil
}

// startFromFile starts the agent when agent.json is there and none runs.
func (u *uplink) startFromFile() {
	if u.agent() != nil {
		return
	}
	cfg, err := agent.LoadConfig(agentConfigPath(u.dataDir))
	if err != nil {
		if !errors.Is(err, agent.ErrNotConfigured) {
			u.log.Warn("cannot read agent.json", "err", err)
		}
		return
	}
	if err := u.start(cfg); err != nil {
		u.log.Warn("cannot start sending matches to the server", "err", err)
	}
}

// watch picks up an agent.json written by `rltracker agent setup` while the
// app runs.
func (u *uplink) watch() {
	for {
		select {
		case <-u.ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
		u.startFromFile()
	}
}

// Stop stops sending matches; what is queued stays in the outbox.
func (u *uplink) Stop() {
	u.mu.Lock()
	stop, done := u.stop, u.done
	u.a, u.stop, u.done = nil, nil, nil
	u.mu.Unlock()
	if stop != nil {
		stop()
		<-done
	}
}

// Flush tries once to upload what is queued (on shutdown).
func (u *uplink) Flush() {
	if a := u.agent(); a != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		a.Flush(ctx)
		cancel()
	}
}

// Destination reads configuration even while the uploader is stopped. Unknown
// identities are recorded as held; unreadable configuration fails the save.
func (u *uplink) Destination() (*domain.Destination, error) {
	cfg, err := agent.LoadConfig(agentConfigPath(u.dataDir))
	if errors.Is(err, agent.ErrNotConfigured) {
		if _, statErr := os.Stat(agentConfigPath(u.dataDir)); errors.Is(statErr, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	return &domain.Destination{Server: cfg.Server, ServerID: cfg.ServerID, AccountID: cfg.AccountID}, nil
}
func (u *uplink) Wake() {
	u.obMu.Lock()
	defer u.obMu.Unlock()
	if u.ob != nil {
		u.ob.Wake()
	}
}

// serverUp reports whether the dashboard of a server answers. A reverse proxy
// in front of a stopped server answers 502/503: that is down too.
func serverUp(base string) bool {
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(strings.TrimRight(base, "/") + "/api/session")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}

// dashboardURL is the dashboard to open: the server's when this PC sends its
// matches to one and it answers, else the local one.
func dashboardURL(server, local string, reachable bool) string {
	if server != "" && (reachable || serverUp(server)) {
		return server
	}
	return local
}

// needsSetup reports whether launching the app opens the installation wizard:
// it was never completed and the Stats API or the autostart is missing.
func needsSetup(cfg config.Config, iniOK, autostart bool) bool {
	return !cfg.SetupDone && !(iniOK && autostart)
}

// launchURL is the page a launch opens (the wizard, or the dashboard).
func launchURL(cfg config.Config, server, local string, reachable bool) string {
	ini := setup.CheckIni(setup.FindInstallDir(cfg.RLInstallDir))
	if needsSetup(cfg, ini.OK, winutil.GetAutostart() != "") {
		return local + "/setup.html"
	}
	return dashboardURL(server, local, reachable)
}

// wizard runs the steps of web/setup.html.
type wizard struct {
	dataDir string
	g       globals
	cfg     *config.Manager
	up      *uplink
	rlPort  int
	local   string
}

var _ server.Setup = (*wizard)(nil)

func (w *wizard) State(ctx context.Context) server.SetupState {
	cfg := w.cfg.Get()
	dir := setup.FindInstallDir(cfg.RLInstallDir)
	st := server.SetupState{InstallDir: dir, Ini: setup.CheckIni(dir), Autostart: winutil.GetAutostart() != "", Done: cfg.SetupDone}
	if s := w.up.Server(); s != "" {
		st.Server = &server.SetupServer{URL: s, Reachable: w.up.Reachable() || serverUp(s)}
	}
	return st
}

func (w *wizard) EnableStatsAPI(rlDir string) (setup.IniStatus, error) {
	cfg := w.cfg.Get()
	if rlDir != "" {
		if !setup.IsInstallDir(rlDir) {
			return setup.IniStatus{}, errors.New(i18n.Tf(lang, "wizard.notInstallDir", rlDir))
		}
		cfg.RLInstallDir = rlDir
		if _, err := w.cfg.Set(cfg); err != nil {
			return setup.IniStatus{}, err
		}
	}
	dir := setup.FindInstallDir(cfg.RLInstallDir)
	if dir == "" {
		return setup.IniStatus{}, errors.New(i18n.T(lang, "wizard.notFound"))
	}
	st, err := enableStatsAPIIn(dir, setup.DefaultPacketSendRate, w.rlPort, nil)
	if errors.Is(err, errIniCheck) {
		err = fmt.Errorf(i18n.T(lang, "setup.checkFailed"), st.Path)
	} else if err != nil {
		err = fmt.Errorf(i18n.T(lang, "setup.enableFailed"), err)
	}
	return st, err
}

func (w *wizard) Connect(ctx context.Context, serverURL, token string) (*server.SetupServer, error) {
	path := agentConfigPath(w.dataDir)
	if serverURL == "" {
		w.up.Stop()
		if err := agent.RemoveConfig(path); err != nil {
			return nil, err
		}
		return nil, nil
	}
	norm, err := agent.NormalizeServer(serverURL)
	if err != nil {
		return nil, err
	}
	if token == "" {
		return nil, errors.New(i18n.T(lang, "wizard.needToken"))
	}
	cfg, err := agent.LoadConfig(path)
	if err != nil {
		cfg = &agent.Config{RLPort: w.rlPort}
	}
	cfg.Server, cfg.Token = norm, token
	hctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	hb, err := newAgentClient(cfg).Heartbeat(hctx, agent.Report{Version: buildinfo.Version})
	cancel()
	if err != nil {
		return nil, fmt.Errorf(i18n.T(lang, "agent.unreachable"), cfg.Server, err)
	}
	cfg.Settings = hb.Settings
	cfg.ServerID, cfg.AccountID = hb.ServerID, hb.User.ID
	if err := agent.SaveConfig(path, cfg); err != nil {
		return nil, err
	}
	if err := w.up.start(cfg); err != nil {
		return nil, err
	}
	return &server.SetupServer{URL: cfg.Server, User: hb.User.Name, Device: hb.Device, Reachable: true}, nil
}

func (w *wizard) Finish(ctx context.Context, autostart bool) (string, error) {
	if autostart {
		exe, err := os.Executable()
		if err != nil {
			return "", err
		}
		if err := winutil.SetAutostart(commandLine(exe, runArgs(w.dataDir, w.g))); err != nil {
			return "", err
		}
	} else if err := winutil.RemoveAutostart(); err != nil {
		return "", err
	}
	cfg := w.cfg.Get()
	cfg.SetupDone = true
	if _, err := w.cfg.Set(cfg); err != nil {
		return "", err
	}
	return dashboardURL(w.up.Server(), w.local, w.up.Reachable()), nil
}
