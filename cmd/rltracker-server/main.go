// Command rltracker-server is the multi-user Rocket Tracker server: players
// sign in through OpenID Connect (Authentik, Keycloak...) and the agents of
// their gaming PCs (`rltracker agent`) send it their matches.
//
// Configuration comes from the environment (Docker-friendly); flags win.
//
//	RT_PUBLIC_URL            external URL, e.g. https://rl.example.com (required)
//	RT_LISTEN                listen address (default :8080)
//	RT_DATA_DIR              database directory (default ./data)
//	RT_OIDC_ISSUER           issuer URL (Authentik: https://auth.example.com/application/o/<slug>/)
//	RT_OIDC_CLIENT_ID        client id
//	RT_OIDC_CLIENT_SECRET    client secret (or RT_OIDC_CLIENT_SECRET_FILE)
//	RT_OIDC_ALLOWED_GROUPS   optional, comma-separated: only members may sign in
//	RT_SESSION_DAYS          session lifetime (default 30)
//	RT_INSECURE_DEV_LOGIN    1: sign in as anyone without a provider (tests only)
//	RT_LOG_LEVEL             debug | info (default) | warn
//	RT_LANG                  fr | en: language of the Discord posts (default en)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // TZ works without zoneinfo in the image (weekly Discord recap)

	"rocket-tracker/internal/i18n"
	"rocket-tracker/internal/server"
	"rocket-tracker/internal/sim"
	"rocket-tracker/internal/store"
	"rocket-tracker/web"
)

// Version is overridable with -ldflags "-X main.Version=...".
var Version = "0.12.0"

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// healthcheck probes /healthz on the listen port (the Docker image has no curl).
func healthcheck() error {
	addr := env("RT_LISTEN", ":8080")
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + addr + "/healthz")
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz: %s", resp.Status)
	}
	return nil
}

func run(args []string) error {
	if len(args) > 0 && args[0] == "import" {
		return cmdImport(args[1:], os.Stdout)
	}
	if len(args) > 0 && args[0] == "healthcheck" {
		return healthcheck()
	}
	fs := flag.NewFlagSet("rltracker-server", flag.ContinueOnError)
	listen := fs.String("listen", env("RT_LISTEN", ":8080"), "listen address")
	dataDir := fs.String("data-dir", env("RT_DATA_DIR", "data"), "data directory")
	publicURL := fs.String("public-url", env("RT_PUBLIC_URL", ""), "external URL of the dashboard")
	issuer := fs.String("oidc-issuer", env("RT_OIDC_ISSUER", ""), "OpenID Connect issuer URL")
	clientID := fs.String("oidc-client-id", env("RT_OIDC_CLIENT_ID", ""), "OpenID Connect client id")
	groups := fs.String("oidc-allowed-groups", env("RT_OIDC_ALLOWED_GROUPS", ""), "comma-separated groups allowed to sign in (empty: all)")
	sessionDays := fs.Int("session-days", atoi(env("RT_SESSION_DAYS", "30"), 30), "session lifetime in days")
	devLogin := fs.Bool("insecure-dev-login", env("RT_INSECURE_DEV_LOGIN", "") == "1", "INSECURE: sign in as anyone (local tests)")
	demo := fs.Bool("demo", false, "with --insecure-dev-login: fill an empty database with demo players")
	logLevel := fs.String("log-level", env("RT_LOG_LEVEL", "info"), "debug, info or warn")
	version := fs.Bool("version", false, "print the version")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *version {
		fmt.Println(Version)
		return nil
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(*logLevel)); err != nil {
		return fmt.Errorf("log level: %w", err)
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	pub := strings.TrimRight(*publicURL, "/")
	if pub == "" {
		return errors.New("RT_PUBLIC_URL (or --public-url) is required, e.g. https://rl.example.com")
	}
	hub := &server.Hub{PublicURL: pub, DevLogin: *devLogin, SessionTTL: time.Duration(*sessionDays) * 24 * time.Hour}
	if *issuer != "" {
		secret := os.Getenv("RT_OIDC_CLIENT_SECRET")
		if f := os.Getenv("RT_OIDC_CLIENT_SECRET_FILE"); f != "" {
			b, err := os.ReadFile(f)
			if err != nil {
				return fmt.Errorf("RT_OIDC_CLIENT_SECRET_FILE: %w", err)
			}
			secret = strings.TrimSpace(string(b))
		}
		if *clientID == "" || secret == "" {
			return errors.New("RT_OIDC_CLIENT_ID and RT_OIDC_CLIENT_SECRET are required with RT_OIDC_ISSUER")
		}
		hub.OIDC = server.NewOIDC(server.OIDCConfig{
			Issuer: *issuer, ClientID: *clientID, ClientSecret: secret, RedirectURL: pub + "/auth/callback",
			AllowedGroups: splitList(*groups),
		})
	} else if !*devLogin {
		return errors.New("no sign-in method: set RT_OIDC_ISSUER, RT_OIDC_CLIENT_ID and RT_OIDC_CLIENT_SECRET")
	}
	if *devLogin {
		log.Warn("INSECURE dev login enabled: anyone can sign in as anyone. Never expose this server.")
	}

	st, err := store.Open(filepath.Join(*dataDir, "rltracker-server.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	if *demo {
		if !*devLogin {
			return errors.New("--demo requires --insecure-dev-login")
		}
		if err := seedDemo(st); err != nil {
			return err
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			if err := st.PurgeSessions(ctx); err != nil && ctx.Err() == nil {
				log.Warn("purge sessions", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	// The container's root filesystem may be read-only: uploads go to the data volume.
	tmpDir := filepath.Join(*dataDir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	// Discord posts go to the webhook each player sets in their settings.
	lang, _ := i18n.FromEnv()
	hub.Discord = &server.Discord{PublicURL: pub, Lang: lang,
		StateFile: filepath.Join(*dataDir, "discord.json"), Log: log.With("component", "discord")}
	go hub.Discord.Run(ctx, st)

	srv := &server.Server{Version: Version, Store: st, Static: web.FS, Log: log.With("component", "http"), Hub: hub, TempDir: tmpDir}
	log.Info("rocket tracker server started", "version", Version, "listen", *listen, "public_url", pub,
		"oidc", hub.OIDC != nil, "data_dir", *dataDir)
	if err := srv.ListenAndServeAddr(ctx, *listen); err != nil {
		return err
	}
	log.Info("rocket tracker server stopped")
	return nil
}

// seedDemo creates a few players with generated matches (empty database only).
func seedDemo(st *store.Store) error {
	ctx := context.Background()
	if us, err := st.Users(ctx); err != nil || len(us) > 0 {
		return err
	}
	for i, name := range []string{"Virgile", "Lucas", "Emma", "Hugo", "Chloé"} {
		u, err := st.UpsertOIDCUser(ctx, "dev:"+strings.ToLower(name), name, name, "")
		if err != nil {
			return err
		}
		ms := sim.Generate(sim.SeedOptions{N: 80 + rand.IntN(200), Days: 60, Seed: uint64(i + 1), MeName: name + "RL"})
		if err := st.User(u.ID).SaveMany(ctx, ms); err != nil {
			return err
		}
	}
	return nil
}

func splitList(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func atoi(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n
	}
	return def
}
