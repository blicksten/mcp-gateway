// MCP Gateway daemon — aggregates MCP servers behind a single endpoint.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"mcp-gateway/internal/api"
	"mcp-gateway/internal/config"
	"mcp-gateway/internal/health"
	"mcp-gateway/internal/lifecycle"
	"mcp-gateway/internal/models"
	"mcp-gateway/internal/proxy"

	"golang.org/x/sync/errgroup"
)

// envNoAuthUnderstood is the environment variable an operator must set
// alongside --no-auth + allow_remote to proceed. See ADR-0003
// §no-auth-escape-hatch.
const envNoAuthUnderstood = "MCP_GATEWAY_I_UNDERSTAND_NO_AUTH"

// Overridden by ldflags at link time; source values are dev-build fallbacks.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
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

	// Context with signal handling.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	g, ctx := errgroup.WithContext(ctx)

	// Start all backends.
	g.Go(func() error {
		if err := lm.StartAll(ctx); err != nil {
			logger.Warn("some backends failed to start", "error", err)
		}
		gw.RebuildTools()
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
			logger.Info("config reloaded")
		}, logger)
	})

	logger.Info("mcp-gateway started", "version", version)

	// Wait for shutdown or error.
	if err := g.Wait(); err != nil && err != context.Canceled {
		return err
	}

	// Graceful shutdown.
	logger.Info("shutting down...")
	lm.StopAll(context.Background())
	logger.Info("mcp-gateway stopped")
	return nil
}
