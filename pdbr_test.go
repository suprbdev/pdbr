package pdbr

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/suprbdev/pdbcore/cache"
	"github.com/suprbdev/pdbcore/inflect"
	"github.com/suprbdev/pdbcore/metrics"
	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/config"
	"github.com/suprbdev/pdbr/internal/server"
)

type renamer struct{}

func (renamer) Name() string  { return "renamer" }
func (renamer) Priority() int { return 300 }
func (renamer) Inflect(kind inflect.Kind, in inflect.Input, next inflect.Next) string {
	if kind == KindResourcePath {
		return "v1_" + next(kind, in)
	}
	return next(kind, in)
}

func TestBuildAPIWithoutDatabase(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.RLS.Enabled = false
	app := New(cfg, WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))), WithVersion("t"), WithPlugins(renamer{}))
	if len(app.Plugins()) != 1+len(builtinPlugins(cfg)) || app.ReadOnly() {
		t.Fatalf("plugins=%d readonly=%v", len(app.Plugins()), app.ReadOnly())
	}
	built, err := app.BuildAPI(context.Background(), testutil.FixtureCatalog())
	if err != nil {
		t.Fatal(err)
	}
	if built.ByPath["v1_users"] == nil {
		t.Errorf("inflection plugin not applied: %v", built.ByPath)
	}
	if _, ok := built.ByPath["v1_users"].Relations["posts"]; !ok {
		t.Error("relation keys must not follow resource_path renames")
	}
	if built.OpenAPI.Info.Version != "t" {
		t.Errorf("version = %q", built.OpenAPI.Info.Version)
	}
	rt, err := app.Runtime(built)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(server.New(cfg.Server, nil, rt, app.Log).Handler())
	defer srv.Close()
	// No pool: the request pipeline runs (route, parse, compile) and fails
	// only at execution with 503 unavailable.
	res, err := http.Get(srv.URL + "/api/v1_users?select=id")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d", res.StatusCode)
	}
	app.SetReadOnly(true)
	if !app.ReadOnly() {
		t.Error("SetReadOnly")
	}
	// The read-only flag reaches the executor of an already-built runtime.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1_users", nil)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed || res.Header.Get("Allow") != "GET, HEAD" {
		t.Errorf("read-only POST: status = %d allow = %q", res.StatusCode, res.Header.Get("Allow"))
	}
}

// TestSwapKeepsInFlightRequests: a request that started on runtime 1 finishes
// on runtime 1 even though the server swapped to runtime 2 in the meantime;
// the next request lands on runtime 2 (watch-mode invariant).
func TestSwapKeepsInFlightRequests(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.RLS.Enabled = false
	app := New(cfg, WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	built, err := app.BuildAPI(context.Background(), testutil.FixtureCatalog())
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	started := make(chan struct{})
	rt1, _ := server.NewRuntime(built, server.RouteHandlerFunc(func(w http.ResponseWriter, _ *http.Request, _ *api.Match) {
		close(started)
		<-release
		w.Header().Set("X-Runtime", "1")
		w.WriteHeader(http.StatusNoContent)
	}))
	rt2, _ := server.NewRuntime(built, server.RouteHandlerFunc(func(w http.ResponseWriter, _ *http.Request, _ *api.Match) {
		w.Header().Set("X-Runtime", "2")
		w.WriteHeader(http.StatusNoContent)
	}))
	s := server.New(cfg.Server, nil, rt1, app.Log)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	type outcome struct {
		res *http.Response
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := http.Get(srv.URL + "/api/users")
		done <- outcome{res, err}
	}()
	<-started
	s.Swap(rt2)
	res2, err := http.Get(srv.URL + "/api/users")
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	close(release)
	first := <-done
	if first.err != nil {
		t.Fatal(first.err)
	}
	first.res.Body.Close()
	if got := first.res.Header.Get("X-Runtime"); got != "1" {
		t.Errorf("in-flight request answered by runtime %q, want 1", got)
	}
	if got := res2.Header.Get("X-Runtime"); got != "2" {
		t.Errorf("post-swap request answered by runtime %q, want 2", got)
	}
}

func cacheBootConfig(t *testing.T) config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "schema.cache")
	if err := cache.Save(path, testutil.FixtureCatalog(), cache.Writer{Product: "pdbr", Version: "test"}); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.RLS.Enabled = false
	cfg.Schema.CachePath = path
	cfg.Database.URL = "postgres://nobody@127.0.0.1:1/none"
	return cfg
}

func TestMetricsWiring(t *testing.T) {
	quiet := WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	cfg := cacheBootConfig(t)
	if app := New(cfg, quiet); app.Metrics() != nil {
		t.Fatal("Metrics() must be nil with metrics.enabled=false")
	}

	cfg.Metrics.Enabled = true
	app := New(cfg, quiet)
	defer app.Close()
	if app.Metrics() == nil {
		t.Fatal("Metrics() is nil with metrics.enabled=true")
	}
	h, err := app.Handler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/api/users?select=id", "/api/users?select=id,posts(id,title,comments(id))"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, p, nil))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, s := range []string{
		"\npdbr_cost_limit_rejections_total 1\n",
		"\npdbr_aggregate_requests_total 0\n",
		"\npdbr_db_pool_max_conns 10\n",
		`pdbr_http_requests_total{method="GET",route="resource",status_class="5xx"} 1`,
		`pdbr_http_requests_total{method="GET",route="resource",status_class="4xx"} 1`,
		"\ngo_goroutines ",
	} {
		if !strings.Contains(body, s) {
			t.Errorf("metrics missing %q", s)
		}
	}
}

func TestWithMetrics(t *testing.T) {
	quiet := WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	reg := prometheus.NewRegistry()
	m, err := metrics.New(metrics.Options{Namespace: "pdbr", Registerer: reg, ConstLabels: prometheus.Labels{"instance_name": "a"}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := cacheBootConfig(t)
	app := New(cfg, quiet, WithMetrics(m))
	defer app.Close()
	if app.Metrics() != m {
		t.Fatal("WithMetrics instance not used")
	}
	h, err := app.Handler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("/metrics with metrics.enabled=false = %d, want 404", rec.Code)
	}
	rec = httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if s := `pdbr_http_requests_total{instance_name="a",method="GET",route="health",status_class="2xx"} 1`; !strings.Contains(rec.Body.String(), s) {
		t.Errorf("embedder registry missing %s", s)
	}

	second := New(cfg, quiet, WithMetrics(m))
	defer second.Close()
	if _, err := second.Handler(context.Background()); err == nil || !strings.Contains(err.Error(), "cost_limit_rejections_total") {
		t.Errorf("second App on one Metrics: err = %v, want a duplicate registration error", err)
	}
}
