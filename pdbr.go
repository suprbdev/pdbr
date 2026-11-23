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
	"github.com/prometheus/client_golang/prometheus"

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

	metrics    *metrics.Metrics
	metricsErr error
	instr      instruments

	// contractInstr holds per-contract instruments, labelled contract, on
	// metricsReg, when the App created its own metrics with contracts
	// configured.
	contractInstr map[string]instruments
	metricsReg    prometheus.Registerer

	watchMu sync.Mutex
	stops   []func()

	watchReady <-chan struct{}

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

// instruments are the Metrics one API records into and its product
// counters.
type instruments struct {
	metrics           *metrics.Metrics
	costRejections    *metrics.Counter
	aggregateRequests *metrics.Counter
}

func newInstruments(m *metrics.Metrics) (instruments, error) {
	in := instruments{metrics: m}
	var err error
	if in.costRejections, err = m.NewCounter("cost_limit_rejections_total",
		"Requests rejected because their estimated cost exceeds limits.max_cost."); err != nil {
		return in, err
	}
	if in.aggregateRequests, err = m.NewCounter("aggregate_requests_total",
		"Requests whose select= uses an aggregate function."); err != nil {
		return in, err
	}
	return in, nil
}

func (a *App) setupMetrics() error {
	if a.metrics == nil && a.Config.Metrics.Enabled && len(a.Config.Contracts) > 0 {
		return a.setupContractMetrics()
	}
	if a.metrics == nil && a.Config.Metrics.Enabled {
		m, err := metrics.New(metrics.Options{Namespace: "pdbr"})
		if err != nil {
			return err
		}
		a.metrics = m
	}
	var err error
	a.instr, err = newInstruments(a.metrics)
	return err
}

// setupContractMetrics creates one Metrics per contract on a shared
// registry, told apart by a contract label; the App's own Metrics is the
// first contract's, whose Handler serves them all.
func (a *App) setupContractMetrics() error {
	reg, err := metrics.NewRegistry()
	if err != nil {
		return err
	}
	a.metricsReg = reg
	a.contractInstr = map[string]instruments{}
	for _, ct := range a.Config.Contracts {
		m, err := metrics.New(metrics.Options{
			Namespace:   "pdbr",
			Registerer:  reg,
			ConstLabels: prometheus.Labels{"contract": ct.Name},
		})
		if err != nil {
			return err
		}
		in, err := newInstruments(m)
		if err != nil {
			return err
		}
		a.contractInstr[ct.Name] = in
		if a.metrics == nil {
			a.metrics, a.instr = m, in
		}
	}
	return nil
}

// instrumentsFor returns the instruments of contract name: its own with
// per-contract metrics, else the App's.
func (a *App) instrumentsFor(name string) instruments {
	if in, ok := a.contractInstr[name]; ok {
		return in
	}
	return a.instr
}

// Metrics returns the Prometheus instruments the App records into: the
// instance given to WithMetrics, else the one created for metrics.enabled,
// else nil (every recording call on a nil *metrics.Metrics is a no-op).
func (a *App) Metrics() *metrics.Metrics { return a.metrics }

// builtinPlugins lists the plugins every App starts with, in priority order
// (smart_comments 50, advanced_filters 150, postgis 160, nested_mutations
// 200); plugins.disabled filters them at hook time.
func builtinPlugins(cfg config.Config) []plugin.Plugin {
	settings := cfg.Plugins.Settings
	return []plugin.Plugin{
		smartcomments.New(),
		advancedfilters.New(settings.AdvancedFilters),
		postgisplugin.New(settings.PostGIS),
		nestedmutations.New(settings.NestedMutations),
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
	register := a.metrics.RegisterPool
	if a.metricsReg != nil {
		register = func(p metrics.PoolStater) error { return metrics.RegisterPoolOn(a.metricsReg, "pdbr", p) }
	}
	if err := register(pool); err != nil {
		pool.Close()
		return err
	}
	a.pool = pool
	return nil
}

// Pool exposes the connection pool (nil before Connect).
func (a *App) Pool() *pgxpool.Pool { return a.pool }

// WatchReady returns a channel closed once the schema watcher started by the
// last Handler or Serve call is listening for DDL; changes committed after it
// closes are never missed. It is already closed when watch mode is off or no
// server has been built.
func (a *App) WatchReady() <-chan struct{} {
	a.watchMu.Lock()
	defer a.watchMu.Unlock()
	if a.watchReady == nil {
		c := make(chan struct{})
		close(c)
		return c
	}
	return a.watchReady
}

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
	return a.buildAPI(ctx, tableKeys(cat), cat, a.Config.Server.BasePath)
}

// BuildContractAPI builds the API of the configured contract name, under
// its base path, from a filtered copy of cat, leaving cat unmodified so
// every contract can be built from one catalog; "" builds the full API
// exactly as BuildAPI does (modifying cat).
func (a *App) BuildContractAPI(ctx context.Context, cat *introspect.Catalog, name string) (*api.Built, error) {
	if name == "" {
		return a.BuildAPI(ctx, cat)
	}
	ct, err := a.Config.Contract(name)
	if err != nil {
		return nil, err
	}
	view, sum := ct.Spec().View(cat)
	a.Log.Debug("contract view", "contract", name, "hidden_tables", sum.Tables, "hidden_columns", sum.Columns,
		"hidden_functions", sum.Functions, "hidden_foreign_keys", sum.ForeignKeys)
	built, err := a.buildAPI(ctx, tableKeys(cat), view, a.Config.ContractBase(ct))
	if err != nil {
		return nil, fmt.Errorf("contract %s: %w", name, err)
	}
	return built, nil
}

// tableKeys returns the "schema.table" keys of cat's tables.
func tableKeys(cat *introspect.Catalog) map[string]bool {
	keys := make(map[string]bool, len(cat.Tables))
	for _, t := range cat.Tables {
		keys[t.Schema+"."+t.Name] = true
	}
	return keys
}

// buildAPI builds the API from cat. introspected holds the table keys of
// the catalog as introspected (before any contract): those missing from
// cat once the catalog hooks ran are the hidden tables (api.Options.
// HiddenTables); it is modified.
func (a *App) buildAPI(ctx context.Context, introspected map[string]bool, cat *introspect.Catalog, basePath string) (*api.Built, error) {
	disabled := a.Config.DisabledPlugins()
	if err := a.Registry.TransformCatalog(ctx, disabled, cat); err != nil {
		return nil, fmt.Errorf("catalog hooks: %w", err)
	}
	hidden := introspected
	for _, t := range cat.Tables {
		delete(hidden, t.Schema+"."+t.Name)
	}
	b := api.New(cat, a.Registry.Inflector(disabled, inflect.Default), api.Options{
		FilterIndexedOnly: a.Config.Filters.IndexedOnly,
		AllowColumns:      a.Config.Filters.AllowColumns,
		Functions:         a.Config.Schema.Functions,
		ExposePartitions:  a.Config.Schema.ExposePartitions,
		HiddenTables:      hidden,
		BasePath:          basePath,
		Title:             a.Config.OpenAPI.Title,
		ServerURL:         a.Config.OpenAPI.ServerURL,
		Schemas:           a.Config.Schema.Schemas,
		MaxPageSize:       a.Config.Limits.MaxPageSize,
		Etags:             a.Config.Server.Etags,
		CSV:               a.Config.Server.CSV,
		Domains:           a.Config.Schema.Domains,
		Aggregates:        a.Config.Aggregates.Enabled,
		JWTType:           a.Config.Auth.MintType,
		AuthMode:          string(a.Config.Auth.Mode),
		AuthRequired:      a.Config.Roles.Anonymous == "" && a.Config.Auth.Mode != auth.ModeNone,
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
	return server.NewRuntime(built, a.routeHandler(built, a.instr))
}

func (a *App) routeHandler(built *api.Built, in instruments) *server.Handler {
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
		Isolation:         pgexec.ParseIsolation(a.Config.TX.Isolation),
		MaxRetries:        a.Config.TX.MaxRetries,
		ClaimsPrefix:      a.Config.Claims.Prefix,
		ClaimsJSONSetting: a.Config.Claims.JSONSetting,
		Detail:            pgexec.ParseDetail(a.Config.Errors.Detail),
		MaxCost:           a.Config.Limits.MaxCost,
		MaxEmbedDepth:     a.Config.Limits.MaxDepth,
		MaxPageSize:       a.Config.Limits.MaxPageSize,
		BasePath:          built.Options.BasePath,
		ReadOnly:          &a.readOnly,
		Mint:              a.mintOptions(),
		Logger:            a.Log,
		Metrics:           in.metrics,
		CostRejections:    in.costRejections,
		AggregateRequests: in.aggregateRequests,
	})
	opts := server.ParseOptions(built, a.Config.Limits.MaxDepth, a.Config.Limits.MaxPageSize, a.Config.Server.Etags, a.Config.Server.CSV)
	opts.AllowUnfilteredMutations = a.Config.Mutations.AllowUnfiltered
	opts.Aggregates = a.Config.Aggregates.Enabled
	opts.TxEnd = a.Config.TX.End
	opts.MaxHeadlineDocument = a.Config.Limits.MaxHeadlineDocument
	opts.PartialBulk = a.Config.TX.Partial.Enabled
	opts.PartialBulkMaxItems = a.Config.TX.Partial.MaxItems
	h := server.NewHandler(built, ex, opts, built.Options.BasePath, a.Log)
	h.CSVRawFormulas = !a.Config.Server.CSVEscapeFormulas
	return h
}

// mintOptions derives the JWT minting options from auth (mint_type is
// "schema.type"; empty disables minting). The secret comes from
// auth.jwt.secret or auth.jwt.secret_file; a secret file that cannot be
// read leaves the secret empty, which jwtmint answers with an error
// (Validate and buildServer reject such a file first).
func (a *App) mintOptions() jwtmint.Options {
	cfg := a.Config.Auth
	if cfg.MintType == "" {
		return jwtmint.Options{}
	}
	secret, _ := cfg.JWT.ResolveSecret()
	schema, name, _ := strings.Cut(cfg.MintType, ".")
	return jwtmint.Options{Schema: schema, Type: name, Secret: secret, Issuer: cfg.JWT.Issuer, Audience: cfg.JWT.Audience}
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
	return a.routeHandler(built, a.instr).CompileSQL(r)
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
// handler exactly as Serve does. With contracts configured, the handler
// serves each contract's API under its base path instead (see
// docs/contracts.md).
func (a *App) Handler(ctx context.Context) (http.Handler, error) {
	srv, err := a.buildServer(ctx)
	if err != nil {
		return nil, err
	}
	return srv.Handler(), nil
}

func (a *App) startupWarnings() {
	for _, w := range confx.IdentityWarnings(a.Config.Auth, a.Config.Roles) {
		a.Log.Warn(w)
	}
}

// authenticator builds the request authenticator from the auth and roles
// sections; an unreadable secret file or an incomplete mode is an error.
func (a *App) authenticator() (*auth.Authenticator, error) {
	cfg, err := confx.AuthConfig(a.Config.Auth, a.Config.Roles)
	if err != nil {
		return nil, err
	}
	cfg.Logger = a.Log
	authn, err := auth.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	return authn, nil
}

// httpServer is what Serve and Handler run: a Server, or a server.Group
// with one Server per contract.
type httpServer interface {
	Handler() http.Handler
	ListenAndServe(ctx context.Context) error
}

// buildServer is the shared boot pipeline behind Serve and Handler.
func (a *App) buildServer(ctx context.Context) (httpServer, error) {
	if a.metricsErr != nil {
		return nil, a.metricsErr
	}
	authn, err := a.authenticator()
	if err != nil {
		return nil, err
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
	if len(a.Config.Contracts) > 0 {
		return a.buildContractServer(ctx, cat, bootHash, authn)
	}
	built, err := a.BuildAPI(ctx, cat)
	if err != nil {
		return nil, err
	}
	rt, err := a.Runtime(built)
	if err != nil {
		return nil, err
	}
	srv := server.New(server.ConfigOf(a.Config), authn, rt, a.Log)
	srv.SetMetrics(a.metrics, a.Config.Metrics)
	srv.SetReadiness(a.pool.Ping)
	if a.Config.Watch.Enabled {
		a.startWatch(ctx, bootHash, func(wctx context.Context, cat *introspect.Catalog) {
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
		})
	}
	return srv, nil
}

// contractRuntime builds the API and runtime of contract name from cat.
func (a *App) contractRuntime(ctx context.Context, cat *introspect.Catalog, name string) (*api.Built, *server.Runtime, error) {
	built, err := a.BuildContractAPI(ctx, cat, name)
	if err != nil {
		return nil, nil, err
	}
	rt, err := server.NewRuntime(built, a.routeHandler(built, a.instrumentsFor(name)))
	if err != nil {
		return nil, nil, err
	}
	return built, rt, nil
}

// buildContractServer builds one API, runtime and Server per contract from
// cat, each under its contract's base path, and mounts them on a
// server.Group; watch mode rebuilds every contract.
func (a *App) buildContractServer(ctx context.Context, cat *introspect.Catalog, bootHash string, authn *auth.Authenticator) (*server.Group, error) {
	group := server.NewGroup(server.ConfigOf(a.Config), a.Log)
	group.SetMetrics(a.metrics, a.Config.Metrics)
	group.SetReadiness(a.pool.Ping)
	servers := make([]*server.Server, len(a.Config.Contracts))
	for i, ct := range a.Config.Contracts {
		_, rt, err := a.contractRuntime(ctx, cat, ct.Name)
		if err != nil {
			return nil, err
		}
		cfg := server.ConfigOf(a.Config)
		cfg.BasePath = a.Config.ContractBase(ct)
		srv := server.New(cfg, authn, rt, a.Log)
		srv.SetMetrics(a.instrumentsFor(ct.Name).metrics, confx.Metrics{})
		group.Add(srv)
		servers[i] = srv
		a.Log.Info("contract ready", "contract", ct.Name, "base_path", cfg.BasePath)
	}
	if a.Config.Watch.Enabled {
		a.startWatch(ctx, bootHash, func(wctx context.Context, cat *introspect.Catalog) {
			for i, ct := range a.Config.Contracts {
				m := a.instrumentsFor(ct.Name).metrics
				nb, nrt, err := a.contractRuntime(wctx, cat, ct.Name)
				m.SchemaRebuilt(err)
				if err != nil {
					a.Log.Error("watch: api rebuild failed", "contract", ct.Name, "err", err)
					continue
				}
				servers[i].Swap(nrt)
				a.Log.Info("watch: api reloaded", "contract", ct.Name, "resources", len(nb.Resources), "rpcs", len(nb.RPCs))
			}
		})
	}
	return group, nil
}

// startWatch runs the schema watcher until ctx ends or the App closes,
// calling rebuild with every changed catalog.
func (a *App) startWatch(ctx context.Context, bootHash string, rebuild func(context.Context, *introspect.Catalog)) {
	wctx, cancel := context.WithCancel(ctx)
	w := &watch.Watcher{
		Pool:         a.pool,
		Schemas:      a.Config.Schema.Schemas,
		Channel:      a.Config.Watch.Channel,
		PollInterval: a.Config.Watch.PollInterval,
		Log:          a.Log,
		TriggerName:  "pdbr_watch",
		InitialHash:  bootHash,
		OnChange:     func(cat *introspect.Catalog) { rebuild(wctx, cat) },
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		go func() {
			select {
			case <-w.Ready():
				a.Log.Info("watch: ready")
			case <-wctx.Done():
			}
		}()
		if err := w.Run(wctx); err != nil {
			a.Log.Error("watch: stopped", "err", err)
		}
	}()
	a.watchMu.Lock()
	a.watchReady = w.Ready()
	a.stops = append(a.stops, func() {
		cancel()
		<-done
	})
	a.watchMu.Unlock()
}
