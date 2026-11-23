package server

import (
	"context"
	"log/slog"
	"net/http"

	"golang.org/x/sync/errgroup"

	"github.com/suprbdev/pdbcore/confx"
	"github.com/suprbdev/pdbcore/httpx"
	"github.com/suprbdev/pdbcore/metrics"

	"github.com/suprbdev/pdbr/internal/config"
)

// Group serves several Servers, one per schema contract, each under its
// own base path (the Server's server.base_path) with its own route table
// and OpenAPI document. /healthz, /readyz and the metrics endpoint are
// served at the root; every other path outside the base paths is a 404
// problem.
type Group struct {
	cfg     config.Server
	log     *slog.Logger
	servers []*Server

	ping       func(context.Context) error
	metrics    *metrics.Metrics
	metricsCfg confx.Metrics
}

// NewGroup returns an empty Group; cfg supplies the listen address,
// request and readyz timeouts. Its BasePath is not used.
func NewGroup(cfg config.Server, log *slog.Logger) *Group {
	if log == nil {
		log = slog.Default()
	}
	return &Group{cfg: cfg, log: log}
}

// Add mounts s under its configured base path. The servers' base paths
// must not overlap; a Server at the root ("") must be the only one. Call
// it before Handler.
func (g *Group) Add(s *Server) { g.servers = append(g.servers, s) }

// SetReadiness makes GET /readyz call ping, as [Server.SetReadiness].
func (g *Group) SetReadiness(ping func(context.Context) error) { g.ping = ping }

// SetMetrics serves m's metrics as [Server.SetMetrics] does: at cfg.Path on
// the main listener, or on a second listener at cfg.Addr. The mounted
// servers instrument their own requests; health and metrics requests at
// the root are not counted.
func (g *Group) SetMetrics(m *metrics.Metrics, cfg confx.Metrics) {
	g.metrics, g.metricsCfg = m, cfg
}

func (g *Group) root() *Server {
	return &Server{cfg: g.cfg, log: g.log, ping: g.ping, metrics: g.metrics, metricsCfg: g.metricsCfg}
}

// Handler returns the HTTP handler.
func (g *Group) Handler() http.Handler {
	root := g.root()
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", readOnlyRoute(httpx.Healthz))
	mux.HandleFunc("/readyz", readOnlyRoute(root.handleReadyz))
	if root.metricsOnMain() {
		mux.Handle(g.metricsCfg.Path, readOnlyRoute(g.metrics.Handler().ServeHTTP))
	}
	atRoot := false
	for _, s := range g.servers {
		base := s.cfg.BasePath
		if base == "" {
			atRoot = true
			mux.Handle("/", s.Handler())
			continue
		}
		mux.Handle(base+"/", s.Handler())
	}
	if !atRoot {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			WriteProblem(w, r, http.StatusNotFound, CodeNotFound, "no such route")
		})
	}
	return root.withRecover(mux)
}

// ListenAndServe runs the group until ctx is cancelled, with the separate
// metrics listener when metrics.addr is set, as [Server.ListenAndServe].
func (g *Group) ListenAndServe(ctx context.Context) error {
	root := g.root()
	bases := make([]string, len(g.servers))
	for i, s := range g.servers {
		bases[i] = s.cfg.BasePath
	}
	attrs := []any{"contracts", bases, "openapi", g.cfg.OpenAPI}
	if root.metricsOnMain() {
		attrs = append(attrs, "metrics_path", g.metricsCfg.Path)
	}
	main := func(ctx context.Context) error {
		return httpx.ListenAndServe(ctx, g.Handler(), httpx.ServeOptions{
			Addr:           g.cfg.Addr,
			RequestTimeout: g.cfg.RequestTimeout,
			Log:            g.log,
			LogAttrs:       attrs,
		})
	}
	if !root.metricsOnOwnAddr() {
		return main(ctx)
	}
	eg, gctx := errgroup.WithContext(ctx)
	eg.Go(func() error { return main(gctx) })
	eg.Go(func() error {
		return httpx.ListenAndServe(gctx, root.MetricsHandler(), httpx.ServeOptions{
			Addr:     g.metricsCfg.Addr,
			Log:      g.log,
			LogAttrs: []any{"metrics_path", g.metricsCfg.Path},
		})
	})
	return eg.Wait()
}
