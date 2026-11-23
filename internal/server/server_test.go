package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/suprbdev/pdbcore/auth"
	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/config"
	"github.com/suprbdev/pdbr/internal/inflect"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testServer(t *testing.T, cfg config.Server, authn *auth.Authenticator, h RouteHandler) *httptest.Server {
	t.Helper()
	return testServerPing(t, cfg, authn, h, nil)
}

func testServerPing(t *testing.T, cfg config.Server, authn *auth.Authenticator, h RouteHandler, ping func(context.Context) error) *httptest.Server {
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
	s := New(cfg, authn, rt, quiet())
	s.SetReadiness(ping)
	srv := httptest.NewServer(s.Handler())
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

func TestReadyz(t *testing.T) {
	cfg := serverCfg()
	cfg.ReadyzTimeout = 100 * time.Millisecond
	ok := testServerPing(t, cfg, nil, nil, func(context.Context) error { return nil })
	if res, body := do(t, "GET", ok.URL+"/readyz", nil); res.StatusCode != 200 || body != `{"status":"ok"}` {
		t.Errorf("readyz with a healthy ping = %d %q", res.StatusCode, body)
	}
	down := testServerPing(t, cfg, nil, nil, func(context.Context) error { return errors.New("refused") })
	res, body := do(t, "GET", down.URL+"/readyz", nil)
	if res.Header.Get("Retry-After") != "1" {
		t.Errorf("503 Retry-After = %q", res.Header.Get("Retry-After"))
	}
	if res.StatusCode != 503 || res.Header.Get("Content-Type") != ContentTypeProblem || problem(t, body).Code != "unavailable" {
		t.Errorf("readyz with a failing ping = %d %q", res.StatusCode, body)
	}
	if res, _ := do(t, "GET", down.URL+"/healthz", nil); res.StatusCode != 200 {
		t.Errorf("healthz must stay liveness, got %d", res.StatusCode)
	}
	if res, _ := do(t, "POST", down.URL+"/readyz", nil); res.StatusCode != 405 {
		t.Errorf("POST readyz = %d", res.StatusCode)
	}
	slow := testServerPing(t, cfg, nil, nil, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	start := time.Now()
	res, _ = do(t, "GET", slow.URL+"/readyz", nil)
	if res.StatusCode != 503 || time.Since(start) > 2*time.Second {
		t.Errorf("hung ping = %d after %s, want 503 within the timeout", res.StatusCode, time.Since(start))
	}
}

func TestReadyzClosedPort(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://u:p@127.0.0.1:1/db?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	cfg := serverCfg()
	cfg.ReadyzTimeout = time.Second
	srv := testServerPing(t, cfg, nil, nil, pool.Ping)
	res, body := do(t, "GET", srv.URL+"/readyz", nil)
	if res.StatusCode != 503 || problem(t, body).Code != "unavailable" {
		t.Errorf("readyz on a closed port = %d %q", res.StatusCode, body)
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
		{"PATCH", "/api/metrics", 405, CodeMethodNotAllowed, "GET, HEAD"},
		{"DELETE", "/api/metrics", 405, CodeMethodNotAllowed, "GET, HEAD"},
		{"POST", "/api/users/1", 405, CodeMethodNotAllowed, "GET, HEAD, PATCH, DELETE"},
		{"POST", "/api/metrics", 405, CodeMethodNotAllowed, "GET, HEAD"},
		{"OPTIONS", "/api/users", 405, CodeMethodNotAllowed, "GET, HEAD, POST, PUT, PATCH, DELETE"},
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
	cfg.Compression = true
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
	res, _ = do(t, "GET", srv.URL+"/api/users", map[string]string{"Origin": "https://app.example.com", "Accept-Encoding": "gzip"})
	if res.Header.Get("Access-Control-Allow-Origin") != "https://app.example.com" {
		t.Errorf("CORS header missing on normal response: %v", res.Header)
	}
	if got := strings.Join(res.Header.Values("Vary"), ", "); !strings.Contains(got, "Origin") || !strings.Contains(got, "Accept-Encoding") {
		t.Errorf("Vary = %q, want Origin and Accept-Encoding", got)
	}
	expose := res.Header.Get("Access-Control-Expose-Headers")
	for _, h := range []string{"Content-Range", "ETag", "Link", "Location", "Preference-Applied", "X-Total-Count", "WWW-Authenticate"} {
		if !strings.Contains(expose, h) {
			t.Errorf("Access-Control-Expose-Headers = %q, missing %s", expose, h)
		}
	}
	res, _ = do(t, "GET", srv.URL+"/api/openapi.json", map[string]string{"Origin": "https://app.example.com", "Accept-Encoding": "gzip"})
	if got := strings.Join(res.Header.Values("Vary"), ", "); !strings.Contains(got, "Origin") || !strings.Contains(got, "Accept-Encoding") {
		t.Errorf("openapi.json Vary = %q, want Origin and Accept-Encoding", got)
	}
	res, _ = do(t, "GET", srv.URL+"/api/users", map[string]string{"Origin": "https://evil.example.com"})
	if res.Header.Get("Access-Control-Allow-Origin") != "" || res.Header.Get("Access-Control-Expose-Headers") != "" {
		t.Error("CORS headers set for disallowed origin")
	}
}

func newAuth(t *testing.T, cfg auth.Config) *auth.Authenticator {
	t.Helper()
	a, err := auth.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func token(t *testing.T, secret string, claims jwt.MapClaims) string {
	t.Helper()
	claims["exp"] = time.Now().Add(time.Hour).Unix()
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAuth(t *testing.T) {
	authn := newAuth(t, auth.Config{
		Mode: auth.ModeJWT, JWTSecret: "s", AnonymousRole: "anonymous",
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
	if res.StatusCode != 401 || problem(t, body).Code != CodeInvalidToken || res.Header.Get("WWW-Authenticate") != `Bearer error="invalid_token"` {
		t.Errorf("bad token = %d %s %v", res.StatusCode, body, res.Header)
	}
	res, body = do(t, "GET", srv.URL+"/api/users", map[string]string{"Authorization": "Basic abc"})
	if res.StatusCode != 401 || problem(t, body).Code != CodeInvalidToken {
		t.Errorf("malformed header = %d %s", res.StatusCode, body)
	}
	tok := token(t, "s", jwt.MapClaims{"role": "app_user", "sub": "7"})
	for _, scheme := range []string{"Bearer", "bearer", "BEARER"} {
		seen = Identity{}
		res, body = do(t, "GET", srv.URL+"/api/users", map[string]string{"Authorization": scheme + " " + tok})
		if res.StatusCode != 204 || seen.Role != "app_user" || seen.Claims["sub"] != "7" {
			t.Errorf("%s token = %d %s, identity %+v", scheme, res.StatusCode, body, seen)
		}
	}
	res, body = do(t, "GET", srv.URL+"/api/users", map[string]string{"Authorization": "Bearer " + token(t, "s", jwt.MapClaims{"role": "admin"})})
	if res.StatusCode != 403 || problem(t, body).Code != CodeRoleNotAllowed || res.Header.Get("WWW-Authenticate") != `Bearer error="insufficient_scope"` {
		t.Errorf("role not allowed = %d %s %v", res.StatusCode, body, res.Header)
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

func TestAuthRequired(t *testing.T) {
	authn := newAuth(t, auth.Config{Mode: auth.ModeJWT, JWTSecret: "s"})
	reached := false
	h := RouteHandlerFunc(func(w http.ResponseWriter, r *http.Request, _ *api.Match) {
		reached = true
		w.WriteHeader(204)
	})
	srv := testServer(t, serverCfg(), authn, h)
	res, body := do(t, "GET", srv.URL+"/api/users", nil)
	if res.StatusCode != 401 || problem(t, body).Code != CodeAuthRequired || res.Header.Get("WWW-Authenticate") != "Bearer" || reached {
		t.Errorf("no credentials = %d %s %v (handler reached %v)", res.StatusCode, body, res.Header, reached)
	}
	res, _ = do(t, "GET", srv.URL+"/api/users", map[string]string{"Authorization": "Bearer " + token(t, "s", jwt.MapClaims{"role": "app_user"})})
	if res.StatusCode != 204 || !reached {
		t.Errorf("with token = %d", res.StatusCode)
	}
}

func TestAuthConnectionRole(t *testing.T) {
	authn := newAuth(t, auth.Config{Mode: auth.ModeJWT, JWTSecret: "s", SkipRoleSwitch: true})
	var seen Identity
	h := RouteHandlerFunc(func(w http.ResponseWriter, r *http.Request, _ *api.Match) {
		seen = IdentityFrom(r.Context())
		w.WriteHeader(204)
	})
	srv := testServer(t, serverCfg(), authn, h)
	res, body := do(t, "GET", srv.URL+"/api/users", map[string]string{"Authorization": "Bearer " + token(t, "s", jwt.MapClaims{"role": "app_user", "sub": "7"})})
	if res.StatusCode != 204 || seen.Role != "" || seen.Claims["sub"] != "7" {
		t.Errorf("roles.switch off = %d %s, identity %+v", res.StatusCode, body, seen)
	}
	res, _ = do(t, "GET", srv.URL+"/api/users", map[string]string{"Authorization": "Bearer garbage"})
	if res.StatusCode != 401 {
		t.Errorf("roles.switch off still verifies credentials: bad token = %d", res.StatusCode)
	}
}

func TestAuthRejectsUnassumableRoles(t *testing.T) {
	authn := newAuth(t, auth.Config{
		Mode: auth.ModeHeaders, HeaderPrefix: "X-Pdbr-Claim-", AnonymousRole: "anonymous",
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")},
	})
	reached := false
	h := RouteHandlerFunc(func(w http.ResponseWriter, r *http.Request, _ *api.Match) {
		reached = true
		w.WriteHeader(204)
	})
	srv := testServer(t, serverCfg(), authn, h)
	for _, role := range []string{"none", "NONE", "public", "pg_read_all_data", "web user"} {
		reached = false
		res, body := do(t, "GET", srv.URL+"/api/users", map[string]string{"X-Pdbr-Claim-Role": role})
		if res.StatusCode != 403 || problem(t, body).Code != CodeRoleNotAllowed || reached ||
			res.Header.Get("WWW-Authenticate") != `Bearer error="insufficient_scope"` {
			t.Errorf("role %q = %d %s (handler reached %v)", role, res.StatusCode, body, reached)
		}
	}
	res, _ := do(t, "GET", srv.URL+"/api/users", map[string]string{"X-Pdbr-Claim-Role": "app_user"})
	if res.StatusCode != 204 || !reached {
		t.Errorf("app_user = %d", res.StatusCode)
	}
}

func TestHeadersAuthUntrustedPeer(t *testing.T) {
	authn := newAuth(t, auth.Config{
		Mode: auth.ModeHeaders, HeaderPrefix: "X-Pdbr-Claim-", AnonymousRole: "anonymous",
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
	})
	reached := false
	h := RouteHandlerFunc(func(w http.ResponseWriter, r *http.Request, _ *api.Match) {
		reached = true
		w.WriteHeader(204)
	})
	srv := testServer(t, serverCfg(), authn, h)
	res, body := do(t, "GET", srv.URL+"/api/users", map[string]string{"X-Pdbr-Claim-Role": "app_user"})
	if res.StatusCode != 401 || problem(t, body).Code != CodeInvalidToken || reached {
		t.Errorf("claim headers from an untrusted peer = %d %s (handler reached %v)", res.StatusCode, body, reached)
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

func TestOpenAPIServing(t *testing.T) {
	cfg := serverCfg()
	cfg.Compression = true
	srv := testServer(t, cfg, nil, nil)
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	get := func(method string, hdr map[string]string) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+"/api/openapi.json", nil)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res, b
	}

	res, plain := get("GET", nil)
	etag := res.Header.Get("ETag")
	if etag == "" || res.Header.Get("Content-Encoding") != "" || bytes.Contains(plain, []byte("\n  ")) {
		t.Fatalf("plain: etag=%q enc=%q", etag, res.Header.Get("Content-Encoding"))
	}

	res, zipped := get("GET", map[string]string{"Accept-Encoding": "gzip"})
	if res.Header.Get("Content-Encoding") != "gzip" || res.Header.Get("ETag") != etag {
		t.Fatalf("gzip: enc=%q etag=%q", res.Header.Get("Content-Encoding"), res.Header.Get("ETag"))
	}
	zr, err := gzip.NewReader(bytes.NewReader(zipped))
	if err != nil {
		t.Fatal(err)
	}
	unz, _ := io.ReadAll(zr)
	if !bytes.Equal(unz, plain) {
		t.Error("gzip body differs from plain body")
	}

	res, body := get("GET", map[string]string{"If-None-Match": etag})
	if res.StatusCode != http.StatusNotModified || len(body) != 0 {
		t.Errorf("If-None-Match = %d (%d bytes)", res.StatusCode, len(body))
	}
	res, _ = get("GET", map[string]string{"If-None-Match": `"other"`})
	if res.StatusCode != http.StatusOK {
		t.Errorf("stale If-None-Match = %d", res.StatusCode)
	}
	res, body = get("HEAD", map[string]string{"Accept-Encoding": "gzip"})
	if res.StatusCode != http.StatusOK || len(body) != 0 || res.Header.Get("Content-Length") != strconv.Itoa(len(zipped)) {
		t.Errorf("HEAD = %d len=%q", res.StatusCode, res.Header.Get("Content-Length"))
	}
}
