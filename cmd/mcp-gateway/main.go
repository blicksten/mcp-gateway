// MCP Gateway daemon — aggregates MCP servers behind a single endpoint.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mcp-gateway/internal/api"
	"mcp-gateway/internal/config"
	"mcp-gateway/internal/health"
	"mcp-gateway/internal/lifecycle"
	"mcp-gateway/internal/models"
	"mcp-gateway/internal/patchstate"
	"mcp-gateway/internal/pidfile"
	"mcp-gateway/internal/plugin"
	"mcp-gateway/internal/proxy"

	"golang.org/x/sync/errgroup"
)

// envNoAuthUnderstood is the environment variable an operator must set
// alongside --no-auth + allow_remote to proceed. See ADR-0003
// §no-auth-escape-hatch.
const envNoAuthUnderstood = "MCP_GATEWAY_I_UNDERSTAND_NO_AUTH"

// version / commit / date can be set in three ways, resolved in this order:
//
//  1. Linker (`go build -ldflags "-X main.version=v1.29.0 -X main.commit=... -X main.date=..."`)
//     — used by .goreleaser.yml. Highest precedence; values arrive here as
//     non-default strings and resolveBuildInfo() leaves them alone.
//
//  2. Module build info (`go install ./cmd/mcp-gateway` from a tagged release —
//     Go ≥1.18). resolveBuildInfo() populates these from
//     runtime/debug.ReadBuildInfo() when the linker did not, so a tagged
//     `go install` yields a real "v1.29.0+abc1234" string.
//
//  3. Dev fallback ("dev", "none", "unknown") when neither is available
//     (e.g., bare `go run` or pre-1.18 builds).
//
// Audit Scope A F-A1 (HIGH, 2026-05-06): the operator's `go install` produced
// version="dev" because only path 1 was wired; this is fixed by enabling path 2.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// resolveBuildInfo upgrades version/commit/date from runtime/debug build info
// when ldflags didn't inject them. Called from main() before any logging.
//
// Behaviour:
//   - When `-ldflags "-X main.version=..."` was used, version != "dev" already
//     and we do nothing (linker wins).
//   - Otherwise: ReadBuildInfo() exposes Go module version (v0.0.0-... for an
//     untagged commit, vX.Y.Z for an installed tagged release) and the VCS
//     stamps Go automatically embeds (vcs.revision, vcs.time, vcs.modified).
func resolveBuildInfo() {
	if version != "dev" {
		// Linker injected ldflags; leave alone.
		return
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	// Module version: empty/("(devel)") for `go run`/untagged; vX.Y.Z for
	// `go install github.com/.../mcp-gateway@vX.Y.Z`.
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		version = info.Main.Version
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if s.Value != "" {
				if len(s.Value) > 12 {
					commit = s.Value[:12]
				} else {
					commit = s.Value
				}
			}
		case "vcs.time":
			if s.Value != "" {
				date = s.Value
			}
		case "vcs.modified":
			if s.Value == "true" && version != "dev" && !strings.Contains(version, "+dirty") {
				version = version + "+dirty"
			}
		}
	}
	// If the module version is still empty but we got a commit, surface it as
	// the version so the UI shows something meaningful instead of "dev".
	if version == "dev" && commit != "none" {
		version = "0.0.0-" + commit
		if strings.Contains(date, "T") {
			version += "-" + strings.SplitN(date, "T", 2)[0]
		}
	}
}

func main() {
	// Resolve module / VCS build info before anyone reads the version vars.
	// No-op when ldflags already injected non-default values.
	resolveBuildInfo()

	var (
		configPath string
		envFile    string
		showVer    bool
		noAuth     bool
	)
	flag.StringVar(&configPath, "config", "", "path to config.json (default ~/.mcp-gateway/config.json)")
	flag.StringVar(&envFile, "env-file", "", "path to .env file for variable expansion (env: MCP_GATEWAY_ENV_FILE)")
	flag.BoolVar(&showVer, "version", false, "print version and exit")
	flag.BoolVar(&noAuth, "no-auth", false, "disable Bearer authentication (DANGEROUS: combined with allow_remote requires "+envNoAuthUnderstood+"=1)")
	flag.Parse()

	// Env var fallback for --env-file (flag overrides env var).
	if envFile == "" {
		envFile = os.Getenv("MCP_GATEWAY_ENV_FILE")
	}

	if showVer {
		fmt.Printf("mcp-gateway %s (commit: %s, built: %s)\n", version, commit, date)
		return
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	if err := run(configPath, envFile, logger, noAuth); err != nil {
		logger.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(configPath, envFile string, logger *slog.Logger, noAuth bool) error {
	// Resolve config path.
	if configPath == "" {
		dir, err := config.DefaultConfigDir()
		if err != nil {
			return err
		}
		configPath = filepath.Join(dir, "config.json")
	}

	localPath := filepath.Join(filepath.Dir(configPath), "config.local.json")

	// Load env file for ${VAR} expansion (does NOT inject into process env).
	envMap, err := config.LoadEnvFile(envFile)
	if err != nil {
		return fmt.Errorf("load env file: %w", err)
	}
	if envFile != "" {
		logger.Info("env file loaded", "path", envFile, "vars", len(envMap))
	}

	// Load config (create default if not found).
	var cfg *models.Config
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		logger.Info("config not found, creating default", "path", configPath)
		if err := config.CreateDefault(configPath); err != nil {
			return fmt.Errorf("create default config: %w", err)
		}
	}

	cfg, err = config.LoadWithLocalExpanded(configPath, localPath, envMap)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger.Info("config loaded",
		"path", configPath,
		"servers", len(cfg.Servers),
		"port", cfg.Gateway.HTTPPort,
		"transports", cfg.Gateway.Transports,
	)

	// Auth bootstrap — T12A.4 + T12A.5.
	// Run startup guards (--no-auth + allow_remote, bearer-required + --no-auth),
	// then load/generate the Bearer token BEFORE http.Server.Serve binds the
	// listener so clients never race with an absent file.
	authCfg, err := setupAuth(cfg, configPath, noAuth, logger)
	if err != nil {
		return err
	}

	// Create components.
	lm := lifecycle.NewManager(cfg, version, logger)
	gw := proxy.New(cfg, lm, version, logger)
	monitor := health.NewMonitor(lm, time.Duration(cfg.Gateway.PingInterval), logger)
	apiServer := api.NewServer(lm, gw, monitor, cfg, configPath, logger, authCfg, version)

	// Phase D.1: PID file — survives crash, stale-detected via HTTP liveness,
	// removed on clean exit (AUDIT M-1, M-2). Written before the errgroup so
	// defer Remove runs after all goroutines stop.
	//
	// Build a probe URL from the configured bind address + port + scheme so
	// stale-reap works after an unclean exit (CV-HIGH fix: without a probe
	// URL, leftover PID files from crashes would block all subsequent starts).
	// For 0.0.0.0 bindings we probe via 127.0.0.1 because the listener accepts
	// on loopback too; https is chosen when both TLS paths are configured.
	pidPath := pidfile.DefaultPath()
	probeHost := cfg.Gateway.BindAddress
	if probeHost == "" || probeHost == "0.0.0.0" {
		probeHost = "127.0.0.1"
	}
	scheme := "http"
	if cfg.Gateway.TLSCertPath != "" && cfg.Gateway.TLSKeyPath != "" {
		scheme = "https"
	}
	probeURL := fmt.Sprintf("%s://%s/api/v1/health",
		scheme, net.JoinHostPort(probeHost, strconv.Itoa(cfg.Gateway.HTTPPort)))
	if err := pidfile.Write(pidPath, probeURL); err != nil {
		return fmt.Errorf("acquire PID file: %w", err)
	}
	defer pidfile.Remove(pidPath) //nolint:errcheck — best-effort cleanup; errors are logged by Remove

	// Phase 16.2: Claude Code plugin regen wiring. Discovery is best-effort
	// — a missing plugin directory is not a daemon-startup failure, it
	// simply leaves regen as a no-op on every mutation.
	pluginDir, err := plugin.Discover()
	switch {
	case err == nil:
		apiServer.SetPluginRegen(pluginDir, plugin.NewRegenerator())
		logger.Info("plugin regen enabled", "plugin_dir", pluginDir)
	case errors.Is(err, plugin.ErrPluginDirNotFound):
		logger.Info("plugin regen disabled", "reason", "plugin directory not discovered",
			"hint", "install the plugin via `claude plugin install` or set $GATEWAY_PLUGIN_DIR")
	default:
		// Non-nil error that is NOT ErrPluginDirNotFound — e.g. env var
		// points at a non-existent path. Log at warn so the operator
		// notices, but still start the daemon.
		logger.Warn("plugin discovery failed", "error", err)
	}

	// Phase 16.3: Claude Code webview patch state. Persist path mirrors
	// the auth.token convention — a dot-directory under the user's home
	// with 0600 on the file. Load() rehydrates state from a previous
	// daemon run and TTL-filters stale entries. The cleaner goroutine
	// prunes expired heartbeats/actions/probes every 30 s; it is stopped
	// during graceful shutdown after the HTTP server closes.
	patchStatePath := patchStatePersistPath(logger)
	ps := patchstate.New(patchStatePath, logger)
	if err := ps.Load(); err != nil {
		logger.Warn("patch-state load failed; starting fresh", "error", err)
	}
	ps.StartCleaner(30 * time.Second)
	apiServer.SetPatchState(ps)
	apiServer.InitClaudeCodeLimiters()
	defer ps.Stop()

	// Context with signal handling.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Phase D.1: wire the signal-cancel into the REST shutdown endpoint so
	// POST /api/v1/shutdown triggers the same clean exit path as SIGTERM.
	apiServer.SetShutdownFn(stop)

	g, ctx := errgroup.WithContext(ctx)

	// Start all backends.
	g.Go(func() error {
		if err := lm.StartAll(ctx); err != nil {
			logger.Warn("some backends failed to start", "error", err)
		}
		gw.RebuildTools()
		// Phase 16.2 (PAL-TD-GAP2): bootstrap plugin .mcp.json on startup.
		// Without this, a fresh daemon whose backends are managed only via
		// config.json (never POSTed through REST) leaves the plugin with
		// the empty checked-in stub until the first REST mutation — which
		// may never happen.
		apiServer.TriggerPluginRegen()
		return nil
	})

	// Health monitor.
	g.Go(func() error {
		return monitor.Run(ctx)
	})

	// HTTP server (REST + MCP HTTP + MCP SSE).
	g.Go(func() error {
		return apiServer.ListenAndServe(ctx)
	})

	// Config watcher.
	g.Go(func() error {
		return config.Watch(ctx, configPath, localPath, envFile, func() {
			// On env file or config change: reload env file then re-expand config.
			currentEnvMap := envMap
			if envFile != "" {
				reloaded, err := config.LoadEnvFile(envFile)
				if err != nil {
					logger.Warn("env file reload failed", "error", err)
					return
				}
				currentEnvMap = reloaded
			}
			newCfg, err := config.LoadWithLocalExpanded(configPath, localPath, currentEnvMap)
			if err != nil {
				logger.Warn("config reload failed", "error", err)
				return
			}
			if err := lm.Reconcile(ctx, newCfg); err != nil {
				logger.Warn("config reconcile failed", "error", err)
				return
			}
			// Config reload is sequential, not atomic across structs. The brief
			// inconsistency window (microseconds) between UpdateConfig calls is
			// acceptable for a localhost daemon — both structs hold their own cfgMu.
			// RebuildTools is placed after both UpdateConfig calls so it reads the
			// new ToolFilter; concurrent RebuildTools from API handlers is safe
			// because Gateway.cfgMu protects the filter snapshot.
			gw.UpdateConfig(newCfg)
			apiServer.UpdateConfig(newCfg)
			gw.RebuildTools()
			// Phase 16.2 (PAL-TD-GAP1): config.json edited outside REST
			// also needs to propagate into the plugin's .mcp.json. Without
			// this, `claude plugin install`-installed clients serve a
			// stale view after every file-level config edit.
			apiServer.TriggerPluginRegen()
			logger.Info("config reloaded")
		}, logger)
	})

	logger.Info("mcp-gateway started", "version", version)

	// Wait for shutdown or error.
	if err := g.Wait(); err != nil && err != context.Canceled {
		return err
	}

	// Graceful shutdown — bounded drain (AUDIT M-4: prevents hung SSE
	// clients or flush operations from blocking exit forever).
	logger.Info("shutting down...")
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer drainCancel()
	// Drain pending patch-state persists BEFORE ps.Stop so the REVIEW-16
	// M-01 durability guarantee holds across a signal-driven daemon exit:
	// any action enqueued between the last mutation and SIGTERM arrival
	// reaches disk before we return. Stop() then terminates the cleaner.
	ps.FlushPersists()
	lm.StopAll(drainCtx)
	logger.Info("mcp-gateway stopped")
	return nil
}

// patchStatePersistPath resolves the on-disk path for patchstate state.
// Convention mirrors auth.token: ~/.mcp-gateway/patch-state.json. Falls
// back to the working directory if HOME cannot be resolved — persistence
// is best-effort and a failed resolve should not prevent daemon start.
func patchStatePersistPath(logger *slog.Logger) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		logger.Warn("user home not resolvable; patch-state will persist to cwd", "error", err)
		return filepath.Join(".", ".mcp-gateway-patch-state.json")
	}
	return filepath.Join(home, ".mcp-gateway", "patch-state.json")
}
