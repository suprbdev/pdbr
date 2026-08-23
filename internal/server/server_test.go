package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/suprbdev/pdbcore/auth"
	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/config"
	"github.com/suprbdev/pdbr/internal/inflect"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testServer(t *testing.T, cfg config.Server, authn *auth.Authenticator, h RouteHandler) *httptest.Server {
	t.Helper()
	built, err := api.New(testutil.FixtureCatalog(), inflect.Default, api.Options{
		FilterIndexedOnly: true, Functions: true, BasePath: cfg.BasePath, Schemas: []string{"public"},
		MaxPageSize: 100, Etags: true, Version: "test", Logger: quiet(),
	}).Build()
	if err != nil {
		t.Fatal(err)
	}
	rt, err := NewRuntime(built, h)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(cfg, authn, rt, quiet()).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func serverCfg() config.Server {
	cfg := config.DefaultConfig().Server
	cfg.RequestTimeout = 5 * time.Second
	return cfg
}

func do(t *testing.T, method, url string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func problem(t *testing.T, body string) Problem {
	t.Helper()
	var p Problem
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("not a problem body: %q: %v", body, err)
	}
	return p
}

func TestHealth(t *testing.T) {
	srv := testServer(t, serverCfg(), nil, nil)
	for _, p := range []string{"/healthz", "/readyz"} {
		res, body := do(t, "GET", srv.URL+p, nil)
		if res.StatusCode != 200 || body != `{"status":"ok"}` {
			t.Errorf("%s = %d %q", p, res.StatusCode, body)
		}
	}
	res, _ := do(t, "GET", srv.URL+"/api/healthz", nil)
	if res.StatusCode != 404 {
		t.Errorf("health under base_path should 404, got %d", res.StatusCode)
	}
}

func TestOpenAPI(t *testing.T) {
	srv := testServer(t, serverCfg(), nil, nil)
	res, body := do(t, "GET", srv.URL+"/api/openapi.json", nil)
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("openapi = %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	var doc struct {
		OpenAPI    string                    `json:"openapi"`
		Servers    []map[string]string       `json:"servers"`
		Components map[string]map[string]any `json:"components"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.OpenAPI != "3.1.0" || doc.Servers[0]["url"] != "/api" {
		t.Errorf("doc = %+v", doc)
	}
	for _, name := range []string{"users", "posts", "comments", "events", "places", "metrics", "event_type", "Problem"} {
		if _, ok := doc.Components["schemas"][name]; !ok {
			t.Errorf("schema %s missing", name)
		}
	}
	res, body = do(t, "HEAD", srv.URL+"/api/openapi.json", nil)
	if res.StatusCode != 200 || body != "" {
		t.Errorf("HEAD openapi = %d %q", res.StatusCode, body)
	}

	cfg := serverCfg()
	cfg.OpenAPI = false
	srv2 := testServer(t, cfg, nil, nil)
	res, _ = do(t, "GET", srv2.URL+"/api/openapi.json", nil)
	if res.StatusCode != 404 {
		t.Errorf("openapi disabled should 404, got %d", res.StatusCode)
	}
}

func TestRouting(t *testing.T) {
	srv := testServer(t, serverCfg(), nil, nil)
	cases := []struct {
		method, path string
		status       int
		code         string
		allow        string
	}{
		{"GET", "/api/users", 501, CodeNotImplemented, ""},
		{"HEAD", "/api/users", 501, "", ""},
		{"POST", "/api/users", 501, CodeNotImplemented, ""},
		{"GET", "/api/users/1", 501, CodeNotImplemented, ""},
		{"GET", "/api/users/by/email/a%40b.c", 501, CodeNotImplemented, ""},
		{"GET", "/api/public/users", 501, CodeNotImplemented, ""},
		{"GET", "/api/rpc/search_posts", 501, CodeNotImplemented, ""},
		{"POST", "/api/rpc/add_numbers", 501, CodeNotImplemented, ""},
		{"GET", "/api/rpc/add_numbers", 405, CodeMethodNotAllowed, "POST"},
		{"PATCH", "/api/users", 405, CodeMethodNotAllowed, "GET, HEAD, POST, PUT"},
		{"DELETE", "/api/users", 405, CodeMethodNotAllowed, "GET, HEAD, POST, PUT"},
		{"POST", "/api/users/1", 405, CodeMethodNotAllowed, "GET, HEAD, PATCH, DELETE"},
		{"POST", "/api/metrics", 405, CodeMethodNotAllowed, "GET, HEAD"},
		{"OPTIONS", "/api/users", 405, CodeMethodNotAllowed, "GET, HEAD, POST, PUT"},
		{"GET", "/api/metrics/1", 404, CodeNotFound, ""},
		{"GET", "/api/nope", 404, CodeNotFound, ""},
		{"GET", "/api/rpc/nope", 404, CodeNotFound, ""},
		{"GET", "/api/users/1/2", 404, CodeNotFound, ""},
		{"GET", "/nope", 404, CodeNotFound, ""},
		{"GET", "/api/", 404, CodeNotFound, ""},
	}
	for _, tc := range cases {
		res, body := do(t, tc.method, srv.URL+tc.path, nil)
		if res.StatusCode != tc.status {
			t.Errorf("%s %s = %d (%s), want %d", tc.method, tc.path, res.StatusCode, body, tc.status)
			continue
		}
		if tc.method == "HEAD" {
			if body != "" {
				t.Errorf("HEAD %s returned a body", tc.path)
			}
			continue
		}
		if res.Header.Get("Content-Type") != ContentTypeProblem {
			t.Errorf("%s %s content-type = %q", tc.method, tc.path, res.Header.Get("Content-Type"))
		}
		p := problem(t, body)
		wantInstance, _ := url.PathUnescape(tc.path)
		if p.Code != tc.code || p.Status != tc.status || p.Instance != wantInstance || p.Type != "about:blank" || p.Title != http.StatusText(tc.status) {
			t.Errorf("%s %s problem = %+v", tc.method, tc.path, p)
		}
		if got := res.Header.Get("Allow"); got != tc.allow {
			t.Errorf("%s %s Allow = %q, want %q", tc.method, tc.path, got, tc.allow)
		}
	}
}

func TestRouteHandlerReceivesMatch(t *testing.T) {
	var got *api.Match
	h := RouteHandlerFunc(func(w http.ResponseWriter, _ *http.Request, m *api.Match) {
		got = m
		w.WriteHeader(204)
	})
	srv := testServer(t, serverCfg(), nil, h)
	res, _ := do(t, "GET", srv.URL+"/api/users/1%2C2?select=id", nil)
	if res.StatusCode != 204 || got == nil || got.Route.Kind != api.RouteRow || got.Key != "1%2C2" || got.Resource.Path != "users" {
		t.Errorf("match = %+v (status %d)", got, res.StatusCode)
	}
}

func TestBasePathVariants(t *testing.T) {
	for _, base := range []string{"", "/v1"} {
		cfg := serverCfg()
		cfg.BasePath = base
		srv := testServer(t, cfg, nil, nil)
		res, _ := do(t, "GET", srv.URL+base+"/users", nil)
		if res.StatusCode != 501 {
			t.Errorf("base %q: /users = %d", base, res.StatusCode)
		}
		res, _ = do(t, "GET", srv.URL+base+"/openapi.json", nil)
		if res.StatusCode != 200 {
			t.Errorf("base %q: openapi = %d", base, res.StatusCode)
		}
		res, body := do(t, "GET", srv.URL+"/healthz", nil)
		if res.StatusCode != 200 || body != `{"status":"ok"}` {
			t.Errorf("base %q: healthz = %d", base, res.StatusCode)
		}
		res, _ = do(t, "GET", srv.URL+base+"/nope", nil)
		if res.StatusCode != 404 {
			t.Errorf("base %q: /nope = %d", base, res.StatusCode)
		}
	}
	res, _ := do(t, "GET", func() string {
		cfg := serverCfg()
		cfg.BasePath = "/v1"
		return testServer(t, cfg, nil, nil).URL
	}()+"/api/users", nil)
	if res.StatusCode != 404 {
		t.Errorf("old base path should 404, got %d", res.StatusCode)
	}
}

func TestCORS(t *testing.T) {
	cfg := serverCfg()
	cfg.CORSOrigins = []string{"https://app.example.com"}
	srv := testServer(t, cfg, nil, nil)
	res, _ := do(t, "OPTIONS", srv.URL+"/api/users", map[string]string{
		"Origin":                        "https://app.example.com",
		"Access-Control-Request-Method": "PATCH",
	})
	if res.StatusCode != 204 {
		t.Fatalf("preflight = %d", res.StatusCode)
	}
	if res.Header.Get("Access-Control-Allow-Origin") != "https://app.example.com" ||
		!strings.Contains(res.Header.Get("Access-Control-Allow-Methods"), "PATCH") {
		t.Errorf("preflight headers = %v", res.Header)
	}
	res, _ = do(t, "GET", srv.URL+"/api/users", map[string]string{"Origin": "https://app.example.com"})
	if res.Header.Get("Access-Control-Allow-Origin") != "https://app.example.com" {
		t.Errorf("CORS header missing on normal response: %v", res.Header)
	}
	res, _ = do(t, "GET", srv.URL+"/api/users", map[string]string{"Origin": "https://evil.example.com"})
	if res.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Error("CORS header set for disallowed origin")
	}
}

func TestAuth(t *testing.T) {
	authn := auth.New(auth.Config{
		Enabled: true, Mode: "jwt", JWTSecret: "s", RoleClaim: "role", AnonymousRole: "anonymous",
		AllowedRoles: []string{"anonymous", "app_user"},
	})
	var seen Identity
	h := RouteHandlerFunc(func(w http.ResponseWriter, r *http.Request, _ *api.Match) {
		seen = IdentityFrom(r.Context())
		w.WriteHeader(204)
	})
	srv := testServer(t, serverCfg(), authn, h)

	res, _ := do(t, "GET", srv.URL+"/api/users", nil)
	if res.StatusCode != 204 || seen.Role != "anonymous" {
		t.Errorf("anonymous = %d role %q", res.StatusCode, seen.Role)
	}
	res, body := do(t, "GET", srv.URL+"/api/users", map[string]string{"Authorization": "Bearer garbage"})
	if res.StatusCode != 401 || problem(t, body).Code != CodeInvalidToken {
		t.Errorf("bad token = %d %s", res.StatusCode, body)
	}
	res, body = do(t, "GET", srv.URL+"/api/users", map[string]string{"Authorization": "Basic abc"})
	if res.StatusCode != 401 || problem(t, body).Code != CodeInvalidToken {
		t.Errorf("malformed header = %d %s", res.StatusCode, body)
	}
	// Health and the OpenAPI document ignore credentials.
	res, _ = do(t, "GET", srv.URL+"/api/openapi.json", map[string]string{"Authorization": "Bearer garbage"})
	if res.StatusCode != 200 {
		t.Errorf("openapi with bad token = %d", res.StatusCode)
	}
	res, _ = do(t, "GET", srv.URL+"/healthz", map[string]string{"Authorization": "Bearer garbage"})
	if res.StatusCode != 200 {
		t.Errorf("healthz with bad token = %d", res.StatusCode)
	}
}

func TestBodyLimit(t *testing.T) {
	cfg := serverCfg()
	cfg.MaxBodyBytes = 10
	srv := testServer(t, cfg, nil, nil)
	req, _ := http.NewRequest("POST", srv.URL+"/api/users", strings.NewReader(strings.Repeat("x", 100)))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 413 || problem(t, string(body)).Code != CodeBodyTooLarge {
		t.Errorf("oversized body = %d %s", res.StatusCode, body)
	}
}

func TestTimeout(t *testing.T) {
	cfg := serverCfg()
	cfg.RequestTimeout = 50 * time.Millisecond
	h := RouteHandlerFunc(func(w http.ResponseWriter, r *http.Request, _ *api.Match) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
		w.WriteHeader(200)
	})
	srv := testServer(t, cfg, nil, h)
	res, body := do(t, "GET", srv.URL+"/api/users", nil)
	if res.StatusCode != 504 || problem(t, body).Code != CodeTimeout {
		t.Errorf("timeout = %d %s", res.StatusCode, body)
	}
}

func TestRecover(t *testing.T) {
	h := RouteHandlerFunc(func(_ http.ResponseWriter, _ *http.Request, _ *api.Match) { panic("boom") })
	srv := testServer(t, serverCfg(), nil, h)
	res, body := do(t, "GET", srv.URL+"/api/users", nil)
	if res.StatusCode != 500 || problem(t, body).Code != CodeInternal {
		t.Errorf("panic = %d %s", res.StatusCode, body)
	}
}

func TestSwap(t *testing.T) {
	built, _ := api.New(testutil.FixtureCatalog(), inflect.Default, api.Options{Schemas: []string{"public"}, BasePath: "/api", Logger: quiet()}).Build()
	rt1, _ := NewRuntime(built, nil)
	s := New(serverCfg(), nil, rt1, quiet())
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	rt2, _ := NewRuntime(built, RouteHandlerFunc(func(w http.ResponseWriter, _ *http.Request, _ *api.Match) { w.WriteHeader(204) }))
	s.Swap(rt2)
	res, _ := do(t, "GET", srv.URL+"/api/users", nil)
	if res.StatusCode != 204 || s.Runtime() != rt2 {
		t.Errorf("swap not effective: %d", res.StatusCode)
	}
}
