package main

import (
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	bolt "go.klarlabs.de/bolt"
	"go.klarlabs.de/mcp"
	"go.klarlabs.de/mcp/middleware"
	"go.klarlabs.de/mcp/transport"

	"github.com/felixgeelhaar/heartbeat/internal/auth"
	"github.com/felixgeelhaar/heartbeat/internal/config"
	"github.com/felixgeelhaar/heartbeat/internal/dashboard"
	"github.com/felixgeelhaar/heartbeat/internal/events"
	"github.com/felixgeelhaar/heartbeat/internal/lifecycle"
	mcptools "github.com/felixgeelhaar/heartbeat/internal/mcp"
	"github.com/felixgeelhaar/heartbeat/internal/storage"
	"github.com/felixgeelhaar/heartbeat/internal/webhook"
	"github.com/felixgeelhaar/heartbeat/sdk"

	// Import plugins — each plugin's init() calls sdk.Register()
	_ "github.com/felixgeelhaar/heartbeat/plugins/jira"
	_ "github.com/felixgeelhaar/heartbeat/plugins/linear"
	_ "github.com/felixgeelhaar/heartbeat/plugins/retro"
)

func main() {
	home, _ := os.UserHomeDir()
	defaultDB := filepath.Join(home, ".heartbeat", "data.db")
	defaultAuth := filepath.Join(home, ".heartbeat", "auth.json")
	defaultConfig := filepath.Join(home, ".heartbeat", "config.yaml")

	dbPath := flag.String("db", defaultDB, "Path to SQLite database file")
	mode := flag.String("mode", "stdio", "Transport mode: stdio or http")
	addr := flag.String("addr", ":8080", "HTTP listen address (only used with --mode http)")
	authConfigPath := flag.String("auth", defaultAuth, "Path to auth config file (only used with --mode http)")
	configPath := flag.String("config", defaultConfig, "Path to config file")
	dashboardAddr := flag.String("dashboard-addr", ":3000", "Dashboard HTTP listen address (empty to disable)")
	dev := flag.Bool("dev", false, "Development mode (colored console logging)")
	flag.Parse()

	// Initialize logger
	var handler bolt.Handler
	if *dev {
		handler = bolt.NewConsoleHandler(os.Stderr)
	} else {
		handler = bolt.NewJSONHandler(os.Stderr)
	}
	logger := bolt.New(handler)

	// Load config
	cfg := config.Load(*configPath)

	store, err := storage.New(*dbPath, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to initialize storage")
	}
	defer store.Close()

	// Create event bus and attach to store
	bus := events.NewBus()
	store.SetEventBus(bus)

	// Set up webhook notifications if configured
	if cfg.WebhookURL != "" {
		wh := webhook.New(cfg.WebhookURL, logger)
		bus.Subscribe(wh)
		logger.Info().Str("url", cfg.WebhookURL).Msg("webhook notifications enabled")
	}

	// Create health check lifecycle manager
	lc, err := lifecycle.New(logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to build lifecycle manager")
	}

	srv := mcptools.NewServer(store, logger, lc)

	// Initialize enabled plugins
	storeReader := storage.NewSDKStoreReader(store)
	var enabledPlugins []sdk.Plugin
	var pluginManifest []sdk.UIEntry

	for _, p := range sdk.All() {
		if !cfg.IsPluginEnabled(p.Name()) {
			logger.Debug().Str("plugin", p.Name()).Msg("plugin disabled")
			continue
		}

		pluginCtx := sdk.PluginContext{
			Store:  storeReader,
			DB:     store.DB(),
			Logger: &boltLoggerAdapter{logger},
			Bus:    &eventBusAdapter{bus},
		}

		if err := p.Init(pluginCtx); err != nil {
			logger.Error().Err(err).Str("plugin", p.Name()).Msg("plugin init failed")
			continue
		}

		if m, ok := p.(sdk.Migrator); ok {
			if err := m.Migrate(store.DB()); err != nil {
				logger.Error().Err(err).Str("plugin", p.Name()).Msg("plugin migration failed")
				continue
			}
		}

		if tp, ok := p.(sdk.ToolProvider); ok {
			tp.RegisterTools(&mcpToolRegistryAdapter{srv})
		}

		if el, ok := p.(sdk.EventListener); ok {
			bus.Subscribe(events.ListenerFunc(func(e events.Event) {
				el.OnEvent(e) // events.Event IS sdk.Event (type alias)
			}))
		}

		if up, ok := p.(sdk.UIProvider); ok {
			pluginManifest = append(pluginManifest, up.UIManifest()...)
		}

		enabledPlugins = append(enabledPlugins, p)
		logger.Info().Str("plugin", p.Name()).Str("version", p.Version()).Msg("plugin loaded")
	}

	ctx, cancel := context.WithCancel(context.Background())
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		cancel()
	}()

	// Start dashboard server if configured
	var dashSrv *dashboard.Server
	if *dashboardAddr != "" {
		dashCfg := dashboard.Config{
			Addr:   *dashboardAddr,
			Store:  store,
			Bus:    bus,
			Logger: logger,
		}
		dashSrv = dashboard.New(dashCfg)

		// Register plugin routes on the dashboard mux
		for _, p := range enabledPlugins {
			if rp, ok := p.(sdk.RouteProvider); ok {
				rp.RegisterRoutes(dashSrv.Mux())
			}
		}

		// Plugin manifest endpoint
		dashSrv.Mux().HandleFunc("GET /api/plugins", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if pluginManifest == nil {
				json.NewEncoder(w).Encode([]sdk.UIEntry{})
			} else {
				json.NewEncoder(w).Encode(pluginManifest)
			}
		})

		go func() {
			if err := dashSrv.Start(); err != nil && err != http.ErrServerClosed {
				logger.Error().Err(err).Msg("dashboard server error")
			}
		}()
	}

	switch *mode {
	case "http":
		var middlewares []middleware.Middleware
		middlewares = append(middlewares,
			middleware.Recover(),
			middleware.Timeout(30*time.Second),
		)

		httpOpts := []mcp.HTTPOption{
			mcp.WithReadTimeout(30 * time.Second),
			mcp.WithWriteTimeout(30 * time.Second),
		}

		// Configure authentication based on config mode.
		//
		// mcp-go v1.19.0 removed in-library auth: token extraction and validation
		// now run at the transport layer via transport.WithRequestContextFn, which
		// stashes the authenticated auth.Identity in the request context. The
		// auth.RequireAuth middleware then enforces it per JSON-RPC method,
		// skipping the handshake methods (initialize, ping) as before.
		switch cfg.Auth.Mode {
		case "oidc":
			oidcValidator, err := auth.NewOIDCValidator(auth.OIDCConfig{
				Issuer:   cfg.Auth.OIDC.Issuer,
				ClientID: cfg.Auth.OIDC.ClientID,
				Scopes:   cfg.Auth.OIDC.Scopes,
			})
			if err != nil {
				logger.Fatal().Err(err).Msg("failed to initialize OIDC auth")
			}
			httpOpts = append(httpOpts,
				transport.WithRequestContextFn(auth.RequestContextFn(oidcValidator.Authenticator())),
			)
			middlewares = append(middlewares, auth.RequireAuth("initialize", "ping"))
			logger.Info().Str("issuer", cfg.Auth.OIDC.Issuer).Msg("OIDC auth enabled")

		default:
			// Token-based auth (legacy)
			if tokenValidator, err := auth.LoadConfig(*authConfigPath); err == nil {
				logger.Info().Str("config", *authConfigPath).Msg("token auth enabled")
				httpOpts = append(httpOpts,
					transport.WithRequestContextFn(auth.RequestContextFn(tokenValidator.Authenticator())),
				)
				middlewares = append(middlewares, auth.RequireAuth("initialize", "ping"))
			} else if !os.IsNotExist(err) {
				logger.Warn().Err(err).Msg("failed to load auth config, running without auth")
			} else {
				logger.Info().Msg("no auth config found, running without auth")
			}
		}

		logger.Info().Str("addr", *addr).Msg("starting HTTP/SSE transport")
		if err := mcp.ServeHTTPWithMiddleware(ctx, srv, *addr,
			httpOpts,
			mcp.WithMiddleware(middlewares...),
		); err != nil {
			logger.Fatal().Err(err).Msg("HTTP server error")
		}
	default:
		logger.Debug().Msg("starting stdio transport")
		if err := mcp.ServeStdio(ctx, srv); err != nil {
			logger.Fatal().Err(err).Msg("stdio server error")
		}
	}

	if dashSrv != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		dashSrv.Shutdown(shutdownCtx)
	}
}

// --- Adapters to bridge internal types to SDK interfaces ---

type boltLoggerAdapter struct {
	l *bolt.Logger
}

type boltLogEventAdapter struct {
	e interface {
		Str(string, string) *bolt.Event
		Int(string, int) *bolt.Event
		Err(error) *bolt.Event
		Msg(string)
	}
}

func (a *boltLoggerAdapter) Info() sdk.LogEvent  { return &boltLogEventAdapter{a.l.Info()} }
func (a *boltLoggerAdapter) Debug() sdk.LogEvent { return &boltLogEventAdapter{a.l.Debug()} }
func (a *boltLoggerAdapter) Error() sdk.LogEvent { return &boltLogEventAdapter{a.l.Error()} }
func (a *boltLoggerAdapter) Warn() sdk.LogEvent  { return &boltLogEventAdapter{a.l.Warn()} }

func (a *boltLogEventAdapter) Str(key, val string) sdk.LogEvent {
	a.e.Str(key, val)
	return a
}
func (a *boltLogEventAdapter) Int(key string, val int) sdk.LogEvent {
	a.e.Int(key, val)
	return a
}
func (a *boltLogEventAdapter) Err(err error) sdk.LogEvent {
	a.e.Err(err)
	return a
}
func (a *boltLogEventAdapter) Msg(msg string) {
	a.e.Msg(msg)
}

type mcpToolRegistryAdapter struct {
	srv *mcp.Server
}

func (a *mcpToolRegistryAdapter) RegisterPluginTool(name, description string, handler any) {
	a.srv.Tool(name).Description(description).Handler(handler)
}

type eventBusAdapter struct {
	bus *events.Bus
}

func (a *eventBusAdapter) Subscribe(l sdk.EventListener) {
	a.bus.Subscribe(events.ListenerFunc(func(e events.Event) {
		l.OnEvent(e) // events.Event IS sdk.Event (type alias)
	}))
}

func (a *eventBusAdapter) Publish(e sdk.Event) {
	a.bus.Publish(e) // sdk.Event IS events.Event (type alias)
}
