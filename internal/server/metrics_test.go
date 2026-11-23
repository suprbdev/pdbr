package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/suprbdev/pdbcore/auth"
	"github.com/suprbdev/pdbcore/confx"
	"github.com/suprbdev/pdbcore/metrics"
	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/config"
	"github.com/suprbdev/pdbr/internal/inflect"
)

func metricsServer(t *testing.T, cfg config.Server, authn *auth.Authenticator, mcfg confx.Metrics) (*Server, *metrics.Metrics) {
	t.Helper()
	built, err := api.New(testutil.FixtureCatalog(), inflect.Default, api.Options{
		FilterIndexedOnly: true, Functions: true, BasePath: cfg.BasePath, Schemas: []string{"public"},
		MaxPageSize: 100, Etags: true, Version: "test", Logger: quiet(),
	}).Build()
	if err != nil {
		t.Fatal(err)
	}
	ok := RouteHandlerFunc(func(w http.ResponseWriter, _ *http.Request, _ *api.Match) { w.WriteHeader(http.StatusOK) })
	rt, err := NewRuntime(built, ok)
	if err != nil {
		t.Fatal(err)
	}
	m, err := metrics.New(metrics.Options{Namespace: "pdbr"})
	if err != nil {
		t.Fatal(err)
	}
	srv := New(cfg, authn, rt, quiet())
	srv.SetMetrics(m, mcfg)
	return srv, m
}

func enabledMetrics() confx.Metrics {
	m := confx.DefaultMetrics()
	m.Enabled = true
	return m
}

func scrape(t *testing.T, h http.Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape = %d %s", rec.Code, rec.Body)
	}
	return rec.Body.String()
}

func wantSeries(t *testing.T, body string, series ...string) {
	t.Helper()
	for _, s := range series {
		if !strings.Contains(body, "\n"+s+"\n") {
			t.Errorf("missing series %s", s)
		}
	}
}

func TestMetricsRouteClasses(t *testing.T) {
	cfg := serverCfg()
	cfg.Compression = true
	authn := auth.New(auth.Config{Enabled: true, Mode: "jwt", JWTSecret: "s", RoleClaim: "role", AnonymousRole: "anonymous"})
	srv, m := metricsServer(t, cfg, authn, enabledMetrics())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	for _, r := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/healthz", 200},
		{"GET", "/readyz", 200},
		{"GET", "/api/openapi.json", 200},
		{"GET", "/api/users", 200},
		{"GET", "/api/users/1", 200},
		{"POST", "/api/rpc/add_numbers", 200},
		{"GET", "/api/rpc/add_numbers", 405},
		{"GET", "/api/nope", 404},
		{"GET", "/nope", 404},
		{"PUT", "/metrics", 405},
	} {
		res, body := do(t, r.method, ts.URL+r.path, nil)
		if res.StatusCode != r.status {
			t.Fatalf("%s %s = %d %s", r.method, r.path, res.StatusCode, body)
		}
	}
	res, _ := do(t, "GET", ts.URL+"/api/users", map[string]string{"Authorization": "Bearer garbage"})
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token = %d", res.StatusCode)
	}

	res, body := do(t, "GET", ts.URL+"/metrics", map[string]string{"Authorization": "Bearer garbage"})
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("metrics = %d %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	if !strings.Contains(body, "pdbr_http_requests_total") {
		t.Fatalf("metrics body lacks pdbr_http_requests_total:\n%s", body)
	}
	wantSeries(t, scrape(t, m.Handler()),
		`pdbr_http_requests_total{method="GET",route="health",status_class="2xx"} 2`,
		`pdbr_http_requests_total{method="GET",route="openapi",status_class="2xx"} 1`,
		`pdbr_http_requests_total{method="GET",route="resource",status_class="2xx"} 2`,
		`pdbr_http_requests_total{method="POST",route="rpc",status_class="2xx"} 1`,
		`pdbr_http_requests_total{method="GET",route="rpc",status_class="4xx"} 1`,
		`pdbr_http_requests_total{method="GET",route="other",status_class="4xx"} 3`,
		`pdbr_http_requests_total{method="PUT",route="metrics",status_class="4xx"} 1`,
		`pdbr_http_requests_total{method="GET",route="metrics",status_class="2xx"} 1`,
		`pdbr_http_requests_in_flight 0`,
	)
}

func TestMetricsEndpointOff(t *testing.T) {
	mcfg := confx.DefaultMetrics()
	srv, m := metricsServer(t, serverCfg(), nil, mcfg)
	h := srv.Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("/metrics with metrics.enabled=false = %d", rec.Code)
	}
	wantSeries(t, scrape(t, m.Handler()), `pdbr_http_requests_total{method="GET",route="other",status_class="4xx"} 1`)

	mcfg.Enabled, mcfg.Addr = true, "127.0.0.1:0"
	srv.SetMetrics(m, mcfg)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("/metrics on the main listener with metrics.addr set = %d", rec.Code)
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func waitGet(t *testing.T, url string) *http.Response {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		res, err := http.Get(url)
		if err == nil {
			res.Body.Close()
			return res
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET %s: %v", url, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestMetricsOwnListener(t *testing.T) {
	cfg := serverCfg()
	cfg.Addr = freeAddr(t)
	mcfg := enabledMetrics()
	mcfg.Addr, mcfg.Path = freeAddr(t), "/prom"
	srv, _ := metricsServer(t, cfg, nil, mcfg)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe(ctx) }()

	if res := waitGet(t, "http://"+cfg.Addr+"/healthz"); res.StatusCode != 200 {
		t.Errorf("main /healthz = %d", res.StatusCode)
	}
	if res := waitGet(t, "http://"+mcfg.Addr+"/prom"); res.StatusCode != 200 {
		t.Errorf("metrics listener /prom = %d", res.StatusCode)
	}
	if res := waitGet(t, "http://"+mcfg.Addr+"/healthz"); res.StatusCode != 404 {
		t.Errorf("metrics listener /healthz = %d", res.StatusCode)
	}
	if res := waitGet(t, "http://"+cfg.Addr+"/prom"); res.StatusCode != 404 {
		t.Errorf("main /prom = %d", res.StatusCode)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ListenAndServe after cancel = %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("ListenAndServe did not return after cancel")
	}
}

func TestMetricsListenerErrorStopsServe(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	cfg := serverCfg()
	cfg.Addr = freeAddr(t)
	mcfg := enabledMetrics()
	mcfg.Addr = busy.Addr().String()
	srv, _ := metricsServer(t, cfg, nil, mcfg)

	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe(context.Background()) }()
	select {
	case err := <-done:
		var opErr *net.OpError
		if !errors.As(err, &opErr) {
			t.Fatalf("ListenAndServe = %v, want the metrics listener's bind error", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("ListenAndServe kept running after the metrics listener failed")
	}
}
