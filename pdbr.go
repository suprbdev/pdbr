// Package pdbr wires the subsystems into a runnable application and is the
// embedding surface for third-party plugins: import pdbr as a library and
// run pdbr.New(cfg, pdbr.WithPlugins(myPlugin)).Serve(ctx).
package pdbr

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pdbcore/auth"
	"github.com/suprbdev/pdbcore/cache"
	"github.com/suprbdev/pdbcore/confx"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/jwtmint"
	"github.com/suprbdev/pdbcore/metrics"
	"github.com/suprbdev/pdbcore/pgexec"
	"github.com/suprbdev/pdbcore/watch"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/config"
	"github.com/suprbdev/pdbr/internal/exec"
	"github.com/suprbdev/pdbr/internal/inflect"
	"github.com/suprbdev/pdbr/internal/plugin"
	"github.com/suprbdev/pdbr/internal/plugins/advancedfilters"
	"github.com/suprbdev/pdbr/internal/plugins/nestedmutations"
	postgisplugin "github.com/suprbdev/pdbr/internal/plugins/postgis"
	"github.com/suprbdev/pdbr/internal/plugins/smartcomments"
	"github.com/suprbdev/pdbr/internal/server"
)

// Version is stamped by the CLI (-ldflags -X main.version) via WithVersion;
// it lands in OpenAPI info.version.
const defaultVersion = "dev"

// App is a configured pdbr instance.
type App struct {
	Config   config.Config
	Registry *plugin.Registry
	Log      *slog.Logger
	Version  string

	pool *pgxpool.Pool

	metrics           *metrics.Metrics
	metricsErr        error
	costRejections    *metrics.Counter
	aggregateRequests *metrics.Counter

	watchMu sync.Mutex
	stops   []func()

	// readOnly lives on the App so one flag is shared by every runtime the
	// App builds — flipping it at runtime survives watch-mode swaps.
	readOnly atomic.Bool
}

// Option customizes an App.
type Option func(*App)

// WithPlugins registers additional plugins (the library embedding pattern).
func WithPlugins(plugins ...plugin.Plugin) Option {
	return func(a *App) {
		for _, p := range plugins {
			a.Registry.Add(p)
		}
	}
}

// WithLogger overrides the default logger.
func WithLogger(l *slog.Logger) Option {
	return func(a *App) { a.Log = l }
}

// WithVersion sets the version reported in the OpenAPI document.
func WithVersion(v string) Option {
	return func(a *App) { a.Version = v }
}

// WithMetrics makes the App record into m instead of the instance it
// creates when metrics.enabled is set, so embedders can build one with
// their own Registerer and ConstLabels. The App registers its pool and its
// product counters on m, so m serves one App only. The endpoint at
// metrics.path is still served only when metrics.enabled is set; without
// it the embedder serves m.Handler() itself.
func WithMetrics(m *metrics.Metrics) Option {
	return func(a *App) { a.metrics = m }
}

// New creates an App with the built-in plugins registered.
func New(cfg config.Config, opts ...Option) *App {
	a := &App{
		Config:   cfg,
		Registry: plugin.NewRegistry(builtinPlugins(cfg)...),
		Log:      newLogger(cfg.Log),
		Version:  defaultVersion,
	}
	for _, o := range opts {
		o(a)
	}
	a.readOnly.Store(cfg.Server.ReadOnly)
	a.metricsErr = a.setupMetrics()
	return a
}

func (a *App) setupMetrics() error {
	if a.metrics == nil && a.Config.Metrics.Enabled {
		m, err := metrics.New(metrics.Options{Namespace: "pdbr"})
		if err != nil {
			return err
		}
		a.metrics = m
	}
	var err error
	if a.costRejections, err = a.metrics.NewCounter("cost_limit_rejections_total",
		"Requests rejected because their estimated cost exceeds server.max_cost."); err != nil {
		return err
	}
	if a.aggregateRequests, err = a.metrics.NewCounter("aggregate_requests_total",
		"Requests whose select= uses an aggregate function."); err != nil {
		return err
	}
	return nil
}

// Metrics returns the Prometheus instruments the App records into: the
// instance given to WithMetrics, else the one created for metrics.enabled,
// else nil (every recording call on a nil *metrics.Metrics is a no-op).
func (a *App) Metrics() *metrics.Metrics { return a.metrics }

// builtinPlugins lists the plugins every App starts with, in priority order
// (smart-comments 50, advanced-filters 150, postgis 160, nested-mutations
// 200); plugins.disabled filters them at hook time.
func builtinPlugins(cfg config.Config) []plugin.Plugin {
	settings := cfg.Plugins.Settings
	return []plugin.Plugin{
		smartcomments.New(settings["smart-comments"]),
		advancedfilters.New(settings["advanced-filters"]),
		postgisplugin.New(settings["postgis"]),
		nestedmutations.New(settings["nested-mutations"]),
	}
}

// Plugins lists registered plugins in execution order (`pdbr plugins list`).
func (a *App) Plugins() []plugin.Plugin { return a.Registry.All() }

// SetReadOnly toggles read-only mode at runtime: when on, every write is
// answered with 405 (code read_only) before it compiles or touches the
// database. Safe for concurrent use; persists across watch-mode swaps.
func (a *App) SetReadOnly(v bool) { a.readOnly.Store(v) }

// ReadOnly reports whether read-only mode is currently on.
func (a *App) ReadOnly() bool { return a.readOnly.Load() }

func newLogger(cfg confx.Log) *slog.Logger {
	var level slog.Level
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if cfg.Format == "json" {
		h = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	return slog.New(h)
}

// Connect opens the pool (idempotent).
func (a *App) Connect(ctx context.Context) error {
	if a.pool != nil {
		return nil
	}
	pool, err := pgexec.Connect(ctx, pgexec.PoolConfig{
		URL:              a.Config.Database.URL,
		MaxConns:         a.Config.Database.MaxConns,
		ConnectTimeout:   a.Config.Database.ConnectTimeout,
		StatementTimeout: a.Config.Database.StatementTimeout,
	})
	if err != nil {
		return err
	}
	if err := a.metrics.RegisterPool(pool); err != nil {
		pool.Close()
		return err
	}
	a.pool = pool
	return nil
}

// Pool exposes the connection pool (nil before Connect).
func (a *App) Pool() *pgxpool.Pool { return a.pool }

// Close stops any running schema watcher, waits for it to release its
// connection, then closes the pool. It is idempotent, needs no prior Serve or
// Handler call, and does not require the context given to them to be
// cancelled first.
func (a *App) Close() {
	a.watchMu.Lock()
	stops := a.stops
	a.stops = nil
	a.watchMu.Unlock()
	for _, stop := range stops {
		stop()
	}
	if a.pool != nil {
		a.pool.Close()
	}
}

// LoadCatalog loads the schema cache when configured, else connects and
// introspects.
func (a *App) LoadCatalog(ctx context.Context) (*introspect.Catalog, error) {
	if p := a.Config.Schema.CachePath; p != "" {
		a.Log.Info("loading schema cache", "path", p)
		return cache.Load(p)
	}
	if err := a.Connect(ctx); err != nil {
		return nil, err
	}
	start := time.Now()
	cat, err := introspect.Introspect(ctx, a.pool, a.Config.Schema.Schemas)
	if err != nil {
		return nil, err
	}
	a.Log.Info("introspected", "tables", len(cat.Tables), "enums", len(cat.Enums),
		"functions", len(cat.Functions), "took", time.Since(start).Round(time.Millisecond))
	return cat, nil
}

// BuildAPI runs the model pipeline: catalog hooks -> inflection -> default
// generation -> API hooks -> Build. Warnings are logged as they arise.
func (a *App) BuildAPI(ctx context.Context, cat *introspect.Catalog) (*api.Built, error) {
	disabled := a.Config.DisabledPlugins()
	if err := a.Registry.TransformCatalog(ctx, disabled, cat); err != nil {
		return nil, fmt.Errorf("catalog hooks: %w", err)
	}
	b := api.New(cat, a.Registry.Inflector(disabled, inflect.Default), api.Options{
		FilterIndexedOnly: a.Config.Filters.IndexedOnly,
		AllowColumns:      a.Config.Filters.AllowColumns,
		Functions:         a.Config.Schema.Functions,
		ExposePartitions:  a.Config.Schema.ExposePartitions,
		BasePath:          a.Config.Server.BasePath,
		Schemas:           a.Config.Schema.Schemas,
		MaxPageSize:       a.Config.Server.MaxPageSize,
		Etags:             a.Config.Server.Etags,
		CSV:               a.Config.Server.CSV,
		Aggregates:        a.Config.Server.Aggregates,
		JWTType:           a.Config.RLS.Auth.JWTType,
		AuthMode:          a.authMode(),
		Version:           a.Version,
		Logger:            a.Log,
	})
	if err := a.Registry.TransformAPI(ctx, disabled, b); err != nil {
		return nil, fmt.Errorf("api hooks: %w", err)
	}
	return b.Build()
}

// Runtime assembles the server runtime for a built API: the compile chain
// (core compiler wrapped by CompileHooks), the executor over the pool, and
// the route handler. The pool may be nil (schema-cache boot without a
// database): requests then fail with 503, --print-sql still works.
func (a *App) Runtime(built *api.Built) (*server.Runtime, error) {
	return server.NewRuntime(built, a.routeHandler(built))
}

func (a *App) routeHandler(built *api.Built) *server.Handler {
	disabled := a.Config.DisabledPlugins()
	chain := a.Registry.CompileChain(disabled, compile.New(built).Compile)
	hooks := a.Registry.RequestHooks(disabled)
	execHooks := make([]exec.RequestHook, len(hooks))
	for i, h := range hooks {
		execHooks[i] = h
	}
	var db exec.DB
	if a.pool != nil {
		db = a.pool
	}
	ex := exec.New(db, built, chain, execHooks, exec.Options{
		TxMutations:       a.Config.TX.Mutations,
		Isolation:         pgexec.ParseIsolation(a.Config.TX.Isolation),
		MaxRetries:        a.Config.TX.MaxRetries,
		RLS:               a.Config.RLS.Enabled,
		ClaimsPrefix:      a.Config.RLS.ClaimsPrefix,
		Detail:            pgexec.ParseDetail(a.Config.Errors.Detail),
		MaxCost:           a.Config.Server.MaxCost,
		MaxEmbedDepth:     a.Config.Server.MaxEmbedDepth,
		MaxPageSize:       a.Config.Server.MaxPageSize,
		BasePath:          a.Config.Server.BasePath,
		ReadOnly:          &a.readOnly,
		Mint:              a.mintOptions(),
		Logger:            a.Log,
		Metrics:           a.metrics,
		CostRejections:    a.costRejections,
		AggregateRequests: a.aggregateRequests,
	})
	opts := server.ParseOptions(built, a.Config.Server.MaxEmbedDepth, a.Config.Server.MaxPageSize, a.Config.Server.Etags, a.Config.Server.CSV)
	opts.AllowUnfilteredMutations = a.Config.Server.AllowUnfilteredMutations
	opts.Aggregates = a.Config.Server.Aggregates
	return server.NewHandler(built, ex, opts, a.Config.Server.BasePath, a.Log)
}

// authMode is rls.auth.mode when RLS is on (credentials are ignored
// otherwise), for the OpenAPI security section.
func (a *App) authMode() string {
	if !a.Config.RLS.Enabled {
		return ""
	}
	return a.Config.RLS.Auth.Mode
}

// mintOptions derives the JWT minting options from rls.auth (jwt_type is
// "schema.type"; empty disables minting).
func (a *App) mintOptions() jwtmint.Options {
	auth := a.Config.RLS.Auth
	if auth.JWTType == "" {
		return jwtmint.Options{}
	}
	schema, name, _ := strings.Cut(auth.JWTType, ".")
	return jwtmint.Options{Schema: schema, Type: name, Secret: auth.JWTSecret, Issuer: auth.JWTIssuer, Audience: auth.JWTAudience}
}

// CompileSQL runs the boot pipeline without connecting to the database
// (schema.cache_path, or a live catalog when no cache is configured) and
// returns the compiled statement for r in the golden format without
// executing it (pdbr request --print-sql). Routing, parse and compile
// failures come back as *exec.Error.
func (a *App) CompileSQL(ctx context.Context, r *http.Request) (string, error) {
	cat, err := a.LoadCatalog(ctx)
	if err != nil {
		return "", err
	}
	built, err := a.BuildAPI(ctx, cat)
	if err != nil {
		return "", err
	}
	return a.routeHandler(built).CompileSQL(r)
}

// Serve runs the HTTP server (and watch mode when enabled) until ctx ends.
func (a *App) Serve(ctx context.Context) error {
	srv, err := a.buildServer(ctx)
	if err != nil {
		return err
	}
	return srv.ListenAndServe(ctx)
}

// Handler runs the boot pipeline (catalog -> plugins -> api.Built ->
// runtime) and returns the http.Handler for embedding in a larger mux.
// Watch mode, when enabled, hot-swaps the runtime behind the returned
// handler exactly as Serve does.
func (a *App) Handler(ctx context.Context) (http.Handler, error) {
	srv, err := a.buildServer(ctx)
	if err != nil {
		return nil, err
	}
	return srv.Handler(), nil
}

func (a *App) startupWarnings() {
	rls := a.Config.RLS
	if !rls.Enabled {
		a.Log.Warn("RLS IS DISABLED — all requests run with the privileged connection role; do not use in production")
	}
	if rls.Enabled && rls.Auth.Mode == "headers" && len(rls.Auth.TrustedProxies) == 0 {
		a.Log.Warn("headers auth mode without rls.auth.trusted_proxies — ANY client reaching this port can forge claims and roles; set trusted_proxies or ensure only the gateway can connect")
	}
	if rls.Enabled && len(rls.AllowedRoles) == 0 && (rls.Auth.Mode == "jwt" || rls.Auth.Mode == "headers") {
		a.Log.Warn("rls.allowed_roles is empty — any role named in a verified claim will be assumed, including privileged database roles; list the roles clients may request")
	}
}

// buildServer is the shared boot pipeline behind Serve and Handler.
func (a *App) buildServer(ctx context.Context) (*server.Server, error) {
	if a.metricsErr != nil {
		return nil, a.metricsErr
	}
	a.startupWarnings()
	cat, err := a.LoadCatalog(ctx)
	if err != nil {
		return nil, err
	}
	var bootHash string
	if a.Config.Watch.Enabled {
		if bootHash, err = cat.Hash(); err != nil {
			return nil, err
		}
	}
	if err := a.Connect(ctx); err != nil {
		return nil, err
	}
	built, err := a.BuildAPI(ctx, cat)
	if err != nil {
		return nil, err
	}
	rt, err := a.Runtime(built)
	if err != nil {
		return nil, err
	}
	srv := server.New(a.Config.Server, auth.New(confx.AuthConfig(a.Config.RLS)), rt, a.Log)
	srv.SetMetrics(a.metrics, a.Config.Metrics)
	if a.Config.Watch.Enabled {
		wctx, cancel := context.WithCancel(ctx)
		w := &watch.Watcher{
			Pool:         a.pool,
			Schemas:      a.Config.Schema.Schemas,
			Channel:      a.Config.Watch.Channel,
			PollInterval: a.Config.Watch.PollInterval,
			Log:          a.Log,
			TriggerName:  "pdbr_watch",
			InitialHash:  bootHash,
			OnChange: func(cat *introspect.Catalog) {
				nb, err := a.BuildAPI(wctx, cat)
				if err != nil {
					a.metrics.SchemaRebuilt(err)
					a.Log.Error("watch: api rebuild failed", "err", err)
					return
				}
				nrt, err := a.Runtime(nb)
				if err != nil {
					a.metrics.SchemaRebuilt(err)
					a.Log.Error("watch: runtime rebuild failed", "err", err)
					return
				}
				srv.Swap(nrt)
				a.metrics.SchemaRebuilt(nil)
				a.Log.Info("watch: api reloaded", "resources", len(nb.Resources), "rpcs", len(nb.RPCs))
			},
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			if err := w.Run(wctx); err != nil {
				a.Log.Error("watch: stopped", "err", err)
			}
		}()
		a.watchMu.Lock()
		a.stops = append(a.stops, func() {
			cancel()
			<-done
		})
		a.watchMu.Unlock()
	}
	return srv, nil
}
