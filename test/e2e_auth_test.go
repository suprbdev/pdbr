package e2e

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbr"
	"github.com/suprbdev/pdbr/internal/config"
)

var authFixturesOnce sync.Once

// authFixtures creates e2e_whoami() (the role a request runs as) and
// e2e_json_claim(k) (claim k read from the request.jwt.claims JSON
// setting, PostgREST style).
func authFixtures(t *testing.T) {
	t.Helper()
	url := os.Getenv("PDBR_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	authFixturesOnce.Do(func() {
		ctx := context.Background()
		conn, err := pgx.Connect(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(ctx)
		for _, ddl := range []string{
			`CREATE OR REPLACE FUNCTION public.e2e_whoami() RETURNS text STABLE LANGUAGE sql AS $$ SELECT current_user::text $$`,
			`CREATE OR REPLACE FUNCTION public.e2e_json_claim(k text) RETURNS text STABLE LANGUAGE sql AS $$
			   SELECT current_setting('request.jwt.claims', true)::json->>k $$`,
			`GRANT EXECUTE ON FUNCTION public.e2e_whoami(), public.e2e_json_claim(text) TO anonymous, app_user`,
		} {
			if _, err := conn.Exec(ctx, ddl); err != nil {
				t.Fatalf("auth fixture: %v", err)
			}
		}
	})
}

func scalar(t *testing.T, url string, hdr map[string]string) any {
	t.Helper()
	res, body := do(t, "GET", url, hdr)
	if res.StatusCode != 200 {
		t.Fatalf("GET %s = %d %s", url, res.StatusCode, body)
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("GET %s: %v: %s", url, err, body)
	}
	return v
}

var ada = map[string]string{"X-Pdbr-Claim-Role": "app_user", "X-Pdbr-Claim-User-Id": "1"}

// TestAnonymousRefusedByDefault: with roles.anonymous empty (the default),
// a request without credentials is 401 auth_required with a Bearer
// challenge, and never reaches the database.
func TestAnonymousRefusedByDefault(t *testing.T) {
	srv := testServer(t, func(c *config.Config) {
		headersAuth(c)
		c.Roles.Anonymous = ""
	})
	res, body := do(t, "GET", srv.URL+"/api/posts?select=id", nil)
	wantProblem(t, res, body, 401, "auth_required")
	if got := res.Header.Get("WWW-Authenticate"); got != "Bearer" {
		t.Errorf("WWW-Authenticate = %q", got)
	}
	res, body = do(t, "GET", srv.URL+"/api/posts?select=id", ada)
	if res.StatusCode != 200 {
		t.Fatalf("with claims = %d %s", res.StatusCode, body)
	}
}

// TestHeadersModeFailClosed: claim headers from a peer outside
// auth.headers.trusted_proxies are refused, and headers mode without any
// trusted proxy does not start.
func TestHeadersModeFailClosed(t *testing.T) {
	srv := testServer(t, func(c *config.Config) {
		headersAuth(c)
		c.Auth.Headers.TrustedProxies = []string{"10.0.0.0/8"}
	})
	res, body := do(t, "GET", srv.URL+"/api/posts?select=id", ada)
	wantProblem(t, res, body, 401, "invalid_token")
	if got := res.Header.Get("WWW-Authenticate"); got != `Bearer error="invalid_token"` {
		t.Errorf("WWW-Authenticate = %q", got)
	}
	res, body = do(t, "GET", srv.URL+"/api/posts?select=id", nil)
	if res.StatusCode != 200 {
		t.Errorf("anonymous from an untrusted peer = %d %s", res.StatusCode, body)
	}

	cfg := config.DefaultConfig()
	cfg.Database.URL = os.Getenv("PDBR_TEST_DATABASE_URL")
	headersAuth(&cfg)
	cfg.Auth.Headers.TrustedProxies = nil
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "auth.headers.trusted_proxies is required") {
		t.Fatalf("Validate = %v", err)
	}
	app := pdbr.New(cfg)
	t.Cleanup(app.Close)
	if _, err := app.Handler(context.Background()); err == nil || !strings.Contains(err.Error(), "trusted proxy") {
		t.Fatalf("Handler without trusted proxies = %v, want a startup error", err)
	}
}

// TestClaimsJSONSetting: claims.json_setting exposes every claim as one
// JSON object (PostgREST's request.jwt.claims), '{}' without credentials,
// next to the per-claim jwt.claims.* settings the fixture policies read.
func TestClaimsJSONSetting(t *testing.T) {
	authFixtures(t)
	srv := testServer(t, func(c *config.Config) {
		headersAuth(c)
		c.Claims.JSONSetting = "request.jwt.claims"
	})
	if got := scalar(t, srv.URL+"/api/rpc/e2e_json_claim?k=user_id", ada); got != "1" {
		t.Errorf("user_id from request.jwt.claims = %#v", got)
	}
	if got := scalar(t, srv.URL+"/api/rpc/e2e_json_claim?k=role", ada); got != "app_user" {
		t.Errorf("role from request.jwt.claims = %#v", got)
	}
	if got := scalar(t, srv.URL+"/api/rpc/e2e_json_claim?k=user_id", nil); got != nil {
		t.Errorf("anonymous user_id = %#v, want null", got)
	}
	var rows []row
	res, body := do(t, "GET", srv.URL+"/api/posts?select=id&limit=100", ada)
	if res.StatusCode != 200 || json.Unmarshal(body, &rows) != nil || len(rows) != 4 {
		t.Errorf("jwt.claims.user_id policy next to the JSON setting = %d %s", res.StatusCode, body)
	}
}

// TestRoleSwitchOff: roles.switch false runs every request as the
// connection role while credentials are still verified and claims set.
func TestRoleSwitchOff(t *testing.T) {
	authFixtures(t)
	on := testServer(t, headersAuth)
	if got := scalar(t, on.URL+"/api/rpc/e2e_whoami", ada); got != "app_user" {
		t.Errorf("roles.switch on: current_user = %#v", got)
	}
	off := testServer(t, func(c *config.Config) {
		headersAuth(c)
		c.Roles.Switch = false
		c.Claims.JSONSetting = "request.jwt.claims"
	})
	conn := scalar(t, off.URL+"/api/rpc/e2e_whoami", ada)
	if conn == "app_user" || conn == "anonymous" || conn == nil {
		t.Errorf("roles.switch off: current_user = %#v, want the connection role", conn)
	}
	if got := scalar(t, off.URL+"/api/rpc/e2e_json_claim?k=user_id", ada); got != "1" {
		t.Errorf("roles.switch off: claims not set: %#v", got)
	}
	res, body := do(t, "GET", off.URL+"/api/rpc/e2e_whoami", map[string]string{"X-Pdbr-Claim-Role": "app_user", "X-Pdbr-Claim-User_id": "1"})
	wantProblem(t, res, body, 401, "invalid_token")
}

// TestCORSEndToEnd: an allowed origin gets Access-Control-Allow-Origin and
// Access-Control-Expose-Headers, and Vary keeps both Origin and
// Accept-Encoding when compression is on.
func TestCORSEndToEnd(t *testing.T) {
	srv := testServer(t, func(c *config.Config) {
		c.Server.CORSOrigins = []string{"https://app.example.com"}
		c.Server.Compression = true
	})
	for _, path := range []string{"/api/users?select=id&limit=1&count=true", "/api/openapi.json"} {
		res, body := do(t, "GET", srv.URL+path, map[string]string{"Origin": "https://app.example.com", "Accept-Encoding": "gzip"})
		if res.StatusCode != 200 && res.StatusCode != 206 {
			t.Fatalf("%s = %d %s", path, res.StatusCode, body)
		}
		if res.Header.Get("Access-Control-Allow-Origin") != "https://app.example.com" {
			t.Errorf("%s: Access-Control-Allow-Origin = %q", path, res.Header.Get("Access-Control-Allow-Origin"))
		}
		expose := res.Header.Get("Access-Control-Expose-Headers")
		for _, h := range []string{"Content-Range", "X-Total-Count", "Link", "ETag", "Location", "Preference-Applied"} {
			if !strings.Contains(expose, h) {
				t.Errorf("%s: Access-Control-Expose-Headers = %q, missing %s", path, expose, h)
			}
		}
		vary := strings.Join(res.Header.Values("Vary"), ", ")
		if !strings.Contains(vary, "Origin") || !strings.Contains(vary, "Accept-Encoding") {
			t.Errorf("%s: Vary = %q", path, vary)
		}
	}
	res, _ := do(t, "GET", srv.URL+"/api/users?select=id&limit=1", map[string]string{"Origin": "https://evil.example.com"})
	if res.Header.Get("Access-Control-Allow-Origin") != "" || res.Header.Get("Access-Control-Expose-Headers") != "" {
		t.Errorf("disallowed origin got CORS headers: %v", res.Header)
	}
}
