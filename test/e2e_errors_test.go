package e2e

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pdbcore/cache"
	"github.com/suprbdev/pdbcore/introspect"

	"github.com/suprbdev/pdbr"
	"github.com/suprbdev/pdbr/internal/config"
	"github.com/suprbdev/pdbr/internal/exec"
)

// errorsFixture adds the objects the docs/errors.md rows need that the shared
// fixture lacks: a slow stable function (timeouts), a RAISE EXCEPTION
// function (P0001), a CHECK constraint (23514), an EXCLUDE constraint
// (23P01), an ON DELETE RESTRICT foreign key (23001 / 23503) and full-text
// functions taking query input (contrib input errors). Dropped again after
// the test.
func errorsFixture(t *testing.T) {
	t.Helper()
	url := os.Getenv("PDBR_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	drop := func() {
		for _, ddl := range []string{
			`DROP FUNCTION IF EXISTS public.e2e_slow(float8)`,
			`DROP FUNCTION IF EXISTS public.e2e_raise()`,
			`DROP TABLE IF EXISTS public.e2e_checked`,
			`DROP TABLE IF EXISTS public.e2e_spans`,
			`DROP TABLE IF EXISTS public.e2e_restrict_child`,
			`DROP TABLE IF EXISTS public.e2e_restrict_parent`,
			`DROP FUNCTION IF EXISTS public.e2e_tsquery(text, text)`,
		} {
			if _, err := conn.Exec(ctx, ddl); err != nil {
				t.Errorf("errors fixture cleanup: %v", err)
			}
		}
	}
	drop()
	t.Cleanup(func() {
		drop()
		conn.Close(ctx)
	})
	for _, ddl := range []string{
		`CREATE FUNCTION public.e2e_slow(seconds float8) RETURNS integer STABLE LANGUAGE sql AS $$
		    SELECT 1 FROM pg_sleep(seconds) $$`,
		`CREATE FUNCTION public.e2e_raise() RETURNS integer STABLE LANGUAGE plpgsql AS $$
		    BEGIN RAISE EXCEPTION 'nope' USING HINT = 'do not'; END $$`,
		`CREATE TABLE public.e2e_checked (id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY, n integer NOT NULL CHECK (n > 0))`,
		`CREATE TABLE public.e2e_spans (id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY, span int4range NOT NULL,
		    EXCLUDE USING gist (span WITH &&))`,
		`INSERT INTO public.e2e_spans (span) VALUES ('[1,10)')`,
		`CREATE TABLE public.e2e_restrict_parent (id integer PRIMARY KEY)`,
		`CREATE TABLE public.e2e_restrict_child (id integer PRIMARY KEY,
		    parent_id integer NOT NULL REFERENCES public.e2e_restrict_parent ON DELETE RESTRICT)`,
		`INSERT INTO public.e2e_restrict_parent VALUES (1)`,
		`INSERT INTO public.e2e_restrict_child VALUES (1, 1)`,
		`CREATE FUNCTION public.e2e_tsquery(cfg text, q text) RETURNS text STABLE LANGUAGE sql AS $$
		    SELECT to_tsquery(cfg::regconfig, q)::text $$`,
	} {
		if _, err := conn.Exec(ctx, ddl); err != nil {
			t.Fatalf("errors fixture: %v\n%s", err, ddl)
		}
	}
}

// boomHook is a RequestHook whose BeforeRequest fails with a plain (non
// UserError) error on GET /comments: the docs' "hook error → 500 internal".
type boomHook struct{}

func (boomHook) Name() string  { return "e2e-boom" }
func (boomHook) Priority() int { return 900 }
func (boomHook) BeforeRequest(ctx context.Context, op *exec.Operation) (context.Context, error) {
	if op.Method == http.MethodGet && op.Path == "/comments" {
		return ctx, errors.New("kaboom: hook failed")
	}
	return ctx, nil
}
func (boomHook) AfterRequest(context.Context, *exec.Operation, *exec.Result) {}

// errorRow is one row of the docs/errors.md tables: a request and the
// status/code it must produce.
type errorRow struct {
	name   string
	method string
	path   string
	body   string
	hdr    map[string]string
	status int
	code   string
	check  func(t *testing.T, res *http.Response, p problem)
}

func runRows(t *testing.T, srv string, rows []errorRow) {
	t.Helper()
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			method := r.method
			if method == "" {
				method = http.MethodGet
			}
			res, body := send(t, method, srv+r.path, r.body, r.hdr)
			p := wantProblem(t, res, body, r.status, r.code)
			if r.check != nil {
				r.check(t, res, p)
			}
		})
	}
}

func allowIs(want string) func(t *testing.T, res *http.Response, p problem) {
	return func(t *testing.T, res *http.Response, _ problem) {
		t.Helper()
		if got := res.Header.Get("Allow"); got != want {
			t.Errorf("Allow = %q, want %q", got, want)
		}
	}
}

func detailIs(want string) func(t *testing.T, res *http.Response, p problem) {
	return func(t *testing.T, _ *http.Response, p problem) {
		t.Helper()
		if p.Detail != want {
			t.Errorf("detail = %q, want %q", p.Detail, want)
		}
	}
}

// TestErrorCodeVocabulary: one assertion per reachable row of the
// docs/errors.md "code vocabulary" table (dev detail; default server).
func TestErrorCodeVocabulary(t *testing.T) {
	errorsFixture(t)
	srv := testServer(t, func(c *config.Config) { c.Server.MaxEmbedDepth = 2 }).URL
	rows := []errorRow{
		{name: "not_found route", path: "/api/nope", status: 404, code: "not_found"},
		{name: "not_found row", path: "/api/users/999999", status: 404, code: "not_found"},
		{name: "invalid_filter", path: "/api/users?id=nope.1", status: 400, code: "invalid_filter"},
		{name: "invalid_select", path: "/api/users?select=id,(", status: 400, code: "invalid_select"},
		{name: "unknown_column", path: "/api/users?select=emial", status: 400, code: "unknown_column"},
		{name: "column_not_filterable", path: "/api/users?full_name=eq.x", status: 400, code: "column_not_filterable"},
		{name: "column_not_orderable", path: "/api/users?order=full_name", status: 400, code: "column_not_orderable"},
		{name: "page_size_exceeded", path: "/api/users?limit=1000", status: 400, code: "page_size_exceeded"},
		{name: "keyset_unsupported", path: "/api/metrics?after=WyJwdWJsaWMubWV0cmljcyIsMV0", status: 400, code: "keyset_unsupported"},
		{name: "cost_exceeded", path: "/api/users?select=id,posts(id,comments(id))", status: 400, code: "cost_exceeded"},
		{name: "embed_depth_exceeded", path: "/api/users?select=id,posts(id,comments(id,posts(id)))&limit=1", status: 400, code: "embed_depth_exceeded"},
		{name: "ambiguous_relation", path: "/api/comments?select=id,comments(id)&limit=1", status: 400, code: "ambiguous_relation"},
		{name: "invalid_body", method: "POST", path: "/api/users", body: `{"nope":1}`, status: 400, code: "invalid_body"},
		{name: "column_readonly", method: "POST", path: "/api/users", body: `{"id":5,"email":"ro@e2e"}`, status: 400, code: "column_readonly"},
		{name: "etag_mismatch", method: "PATCH", path: "/api/users/1", body: `{"full_name":"x"}`, hdr: map[string]string{"If-Match": `W/"stale"`}, status: 412, code: "etag_mismatch"},
		{name: "not_acceptable", path: "/api/users", hdr: map[string]string{"Accept": "application/xml"}, status: 406, code: "not_acceptable"},
		{name: "unsupported_media_type", method: "POST", path: "/api/users", body: `{"email":"x"}`, hdr: map[string]string{"Content-Type": "text/plain"}, status: 415, code: "unsupported_media_type"},
		{name: "unknown_relation", path: "/api/users?select=id,nope(id)", status: 400, code: "unknown_relation"},
		{name: "invalid_cursor", path: "/api/users?after=zzz", status: 400, code: "invalid_cursor"},
		{name: "method_not_allowed", method: "POST", path: "/api/metrics", status: 405, code: "method_not_allowed", check: allowIs("GET, HEAD")},
		{name: "unknown_argument", path: "/api/rpc/best_mood?nope=1", status: 400, code: "unknown_argument"},
		{name: "missing_argument", path: "/api/rpc/describe_mood", status: 400, code: "missing_argument", check: detailIs(`function "describe_mood": missing required argument(s): m`)},
		{name: "unfiltered_mutation", method: "DELETE", path: "/api/users", status: 400, code: "unfiltered_mutation"},
		{name: "aggregates_disabled", path: "/api/posts?select=count()", status: 400, code: "aggregates_disabled"},
		{name: "if_match_unsupported", method: "DELETE", path: "/api/users?id=eq.999999", hdr: map[string]string{"If-Match": `W/"x"`}, status: 400, code: "if_match_unsupported"},
		{name: "invalid_preference", method: "DELETE", path: "/api/users/999999", hdr: map[string]string{"Prefer": "handling=strict, tx=commit"}, status: 400, code: "invalid_preference"},
		{name: "max_affected_exceeded", method: "PATCH", path: "/api/users?id=gt.0", body: `{"full_name":"x"}`, hdr: map[string]string{"Prefer": "handling=strict, max-affected=1"}, status: 400, code: "max_affected_exceeded"},
		{name: "pg sqlstate", path: "/api/users?settings=jpe.$[", status: 400, code: "pg:42601", check: func(t *testing.T, _ *http.Response, p problem) {
			if p.PG["sqlstate"] != "42601" {
				t.Errorf("pg object = %v", p.PG)
			}
		}},
	}
	runRows(t, srv, rows)

	// HEAD errors: same status and headers, no body.
	res, body := do(t, "HEAD", srv+"/api/metrics?after=x", nil)
	if res.StatusCode != 400 || len(body) != 0 || res.Header.Get("Content-Type") != "application/problem+json" {
		t.Errorf("HEAD error = %d %q %v", res.StatusCode, body, res.Header)
	}
	// instance is the path without the query string.
	res, body = get(t, srv+"/api/users?select=emial")
	if p := wantProblem(t, res, body, 400, "unknown_column"); p.Instance != "/api/users" || p.Title != "Bad Request" || p.Type != "about:blank" {
		t.Errorf("problem shape = %+v", p)
	}
}

// TestErrorCodesNeedingConfig: rows that need a non-default server:
// read_only, body_too_large, timeout, internal, unavailable, invalid_token,
// role_not_allowed.
func TestErrorCodesNeedingConfig(t *testing.T) {
	errorsFixture(t)
	t.Run("read_only", func(t *testing.T) {
		srv := testServer(t, func(c *config.Config) { c.Server.ReadOnly = true }).URL
		runRows(t, srv, []errorRow{
			{name: "table", method: "POST", path: "/api/users", body: `{"email":"x"}`, status: 405, code: "read_only", check: allowIs("GET, HEAD")},
			{name: "volatile rpc", method: "POST", path: "/api/rpc/add_numbers", body: `{"a":1,"b":2}`, status: 405, code: "read_only", check: func(t *testing.T, res *http.Response, _ problem) {
				if _, ok := res.Header["Allow"]; !ok || res.Header.Get("Allow") != "" {
					t.Errorf("Allow = %q (want present and empty: no method is available)", res.Header.Get("Allow"))
				}
			}},
			{name: "read still 405 method_not_allowed", method: "PATCH", path: "/api/metrics", status: 405, code: "method_not_allowed", check: allowIs("GET, HEAD")},
		})
	})
	t.Run("body_too_large", func(t *testing.T) {
		srv := testServer(t, func(c *config.Config) { c.Server.MaxBodyBytes = 64 }).URL
		big := `{"email":"` + strings.Repeat("x", 100) + `"}`
		runRows(t, srv, []errorRow{
			{name: "content-length", method: "POST", path: "/api/users", body: big, status: 413, code: "body_too_large"},
		})
	})
	t.Run("timeout", func(t *testing.T) {
		srv := testServer(t, func(c *config.Config) { c.Server.RequestTimeout = 300 * time.Millisecond }).URL
		runRows(t, srv, []errorRow{
			{name: "request_timeout", path: "/api/rpc/e2e_slow?seconds=2", status: 504, code: "timeout"},
		})
	})
	t.Run("statement_timeout", func(t *testing.T) {
		srv := testServer(t, func(c *config.Config) { c.Database.StatementTimeout = 200 * time.Millisecond }).URL
		runRows(t, srv, []errorRow{
			{name: "pg:57014", path: "/api/rpc/e2e_slow?seconds=2", status: 504, code: "pg:57014"},
		})
	})
	t.Run("internal", func(t *testing.T) {
		url := os.Getenv("PDBR_TEST_DATABASE_URL")
		for _, detail := range []string{"dev", "prod"} {
			cfg := config.DefaultConfig()
			cfg.Database.URL = url
			cfg.RLS.Enabled = false
			cfg.Errors.Detail = detail
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
			app := pdbr.New(cfg, pdbr.WithPlugins(boomHook{}))
			t.Cleanup(app.Close)
			h, err := app.Handler(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(h)
			t.Cleanup(srv.Close)
			want := "internal error"
			if detail == "dev" {
				want = "kaboom: hook failed"
			}
			runRows(t, srv.URL, []errorRow{
				{name: "hook error " + detail, path: "/api/comments", status: 500, code: "internal", check: detailIs(want)},
			})
		}
	})
	t.Run("unavailable", func(t *testing.T) {
		// Boot from a schema cache with a database nobody listens on: the
		// pool acquire fails with a connection error, no SQLSTATE.
		url := os.Getenv("PDBR_TEST_DATABASE_URL")
		ctx := context.Background()
		pool, err := pgxpool.New(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		cat, err := introspect.Introspect(ctx, pool, []string{"public"})
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "schema.cache")
		if err := cache.Save(path, cat); err != nil {
			t.Fatal(err)
		}
		srv := testServer(t, func(c *config.Config) {
			c.Schema.CachePath = path
			c.Database.URL = "postgres://pdbr:pdbr@127.0.0.1:1/pdbr_test?connect_timeout=1"
			c.Errors.Detail = "prod"
		}).URL
		runRows(t, srv, []errorRow{
			{name: "pool acquire", path: "/api/users?limit=1", status: 503, code: "unavailable", check: func(t *testing.T, res *http.Response, p problem) {
				if p.Detail != "database unavailable" || res.Header.Get("Retry-After") != "" {
					t.Errorf("problem = %+v headers %v", p, res.Header)
				}
			}},
		})
	})
	t.Run("invalid_token", func(t *testing.T) {
		srv := testServer(t, func(c *config.Config) {
			c.RLS.Enabled = true
			c.RLS.Auth.Mode = "jwt"
			c.RLS.Auth.JWTSecret = "e2e-secret-e2e-secret-e2e-secret"
		}).URL
		runRows(t, srv, []errorRow{
			{name: "bad bearer", path: "/api/users", hdr: map[string]string{"Authorization": "Bearer not.a.jwt"}, status: 401, code: "invalid_token"},
		})
	})
	t.Run("role_not_allowed", func(t *testing.T) {
		srv := testServer(t, func(c *config.Config) {
			c.RLS.Enabled = true
			c.RLS.Auth.Mode = "headers"
			c.RLS.AllowedRoles = []string{"app_user"}
		}).URL
		runRows(t, srv, []errorRow{
			{name: "role outside allowlist", path: "/api/posts", hdr: map[string]string{"X-Pdbr-Claim-Role": "postgres"}, status: 403, code: "role_not_allowed"},
			{name: "42501 permission", path: "/api/comments", status: 403, code: "pg:42501"},
		})
	})
}

// TestErrorStatusMapping: one assertion per reachable row of the
// docs/errors.md "Status mapping" table not already pinned above.
func TestErrorStatusMapping(t *testing.T) {
	errorsFixture(t)
	srv := testServer(t, func(c *config.Config) { c.TX.MaxRetries = 0 }).URL
	rows := []errorRow{
		{name: "22* data exception", path: "/api/users?email=eq.%FF", status: 400, code: "pg:22021"},
		{name: "23502 not null", method: "POST", path: "/api/posts", body: `{"author_id":1,"title":null}`, status: 400, code: "pg:23502"},
		{name: "23514 check", method: "POST", path: "/api/e2e_checked", body: `{"n":0}`, status: 400, code: "pg:23514"},
		{name: "42* other", path: "/api/users?settings=jpe.$[", status: 400, code: "pg:42601"},
		{name: "404 keyed mutation", method: "PATCH", path: "/api/users/999999", body: `{"full_name":"x"}`, status: 404, code: "not_found"},
		{name: "405 view patch", method: "PATCH", path: "/api/metrics", body: `{"name":"x"}`, status: 405, code: "method_not_allowed", check: allowIs("GET, HEAD")},
		{name: "400 unfiltered bulk patch", method: "PATCH", path: "/api/users", body: `{"full_name":"x"}`, status: 400, code: "unfiltered_mutation"},
		{name: "405 row route", method: "POST", path: "/api/users/1", body: `{}`, status: 405, code: "method_not_allowed", check: allowIs("GET, HEAD, PATCH, DELETE")},
		{name: "405 volatile via GET", path: "/api/rpc/add_numbers", status: 405, code: "method_not_allowed", check: allowIs("POST")},
		{name: "405 stable rpc", method: "DELETE", path: "/api/rpc/search_posts", status: 405, code: "method_not_allowed", check: allowIs("GET, HEAD, POST")},
		{name: "405 openapi.json", method: "POST", path: "/api/openapi.json", status: 405, code: "method_not_allowed", check: allowIs("GET, HEAD")},
		{name: "405 healthz", method: "DELETE", path: "/healthz", status: 405, code: "method_not_allowed", check: allowIs("GET, HEAD")},
		{name: "409 23505 unique", method: "POST", path: "/api/users", body: `{"email":"ada@example.com"}`, status: 409, code: "pg:23505"},
		{name: "409 23503 fk", method: "POST", path: "/api/posts", body: `{"author_id":999999,"title":"x"}`, status: 409, code: "pg:23503"},
		{name: "409 23P01 exclusion", method: "POST", path: "/api/e2e_spans", body: `{"span":"[5,6)"}`, status: 409, code: "pg:23P01"},
		{name: "409 ON DELETE RESTRICT", method: "DELETE", path: "/api/e2e_restrict_parent/1", status: 409, code: restrictCode(t)},
		{name: "400 PostGIS mixed SRID", path: "/api/places?location=st_intersects.SRID%3D3857%3BPOINT(-0.1281%2051.508)", status: 400, code: "pg:XX000"},
		{name: "400 PostGIS parse error", path: "/api/places?location=st_intersects.POINT(1%202%203%204%205)", status: 400, code: "pg:XX000"},
		{name: "400 tsquery syntax", path: "/api/rpc/e2e_tsquery?cfg=english&q=a%20%26%26", status: 400, code: "pg:42601"},
		{name: "400 unknown text search configuration", path: "/api/rpc/e2e_tsquery?cfg=nope&q=a", status: 400, code: "pg:42704"},
		{name: "500 P0001 raise", path: "/api/rpc/e2e_raise", status: 500, code: "pg:P0001", check: func(t *testing.T, _ *http.Response, p problem) {
			if p.Detail != "nope" || p.PG["hint"] != "do not" {
				t.Errorf("dev P0001 = %+v", p)
			}
		}},
	}
	runRows(t, srv, rows)

	// 40001 after retries are exhausted -> 409 pg:40001. retry_probe raises
	// on odd sequence values, so one of two consecutive calls fails.
	failed := false
	for i := 0; i < 2 && !failed; i++ {
		res, body := send(t, "POST", srv+"/api/rpc/retry_probe", "", nil)
		if res.StatusCode == 409 {
			wantProblem(t, res, body, 409, "pg:40001")
			failed = true
		} else if res.StatusCode != 200 {
			t.Fatalf("retry_probe = %d %s", res.StatusCode, body)
		}
	}
	if !failed {
		t.Error("retry_probe never surfaced 40001 with max_retries=0")
	}
}

// restrictCode is the SQLSTATE of an ON DELETE RESTRICT violation:
// PostgreSQL 18 reports 23001, older servers 23503.
func restrictCode(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, os.Getenv("PDBR_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var num int
	if err := conn.QueryRow(ctx, `SELECT current_setting('server_version_num')::int`).Scan(&num); err != nil {
		t.Fatal(err)
	}
	if num >= 180000 {
		return "pg:23001"
	}
	return "pg:23503"
}

// TestErrorDetailLevels: the errors.detail table — pg object only in dev,
// prod passes 22*/23* and contrib input messages through, renders PostGIS
// input errors as "invalid geometry" and hides the rest, strict hides
// everything.
func TestErrorDetailLevels(t *testing.T) {
	errorsFixture(t)
	dup := errorRow{method: "POST", path: "/api/users", body: `{"email":"ada@example.com"}`, hdr: map[string]string{"X-Pdbr-Claim-Role": "app_user"}, status: 409, code: "pg:23505"}
	perm := errorRow{path: "/api/comments", status: 403, code: "pg:42501"}
	raise := errorRow{path: "/api/rpc/e2e_raise", status: 500, code: "pg:P0001"}
	syntax := errorRow{path: "/api/users?settings=jpe.$[", status: 400, code: "pg:42601"}
	geom := errorRow{path: "/api/places?location=st_intersects.SRID%3D3857%3BPOINT(-0.1281%2051.508)", status: 400, code: "pg:XX000"}
	tsq := errorRow{path: "/api/rpc/e2e_tsquery?cfg=english&q=a%20%26%26", status: 400, code: "pg:42601"}
	for level, want := range map[string]struct{ dup, perm, raise, syntax, geom, tsq string }{
		"dev":    {`duplicate key value violates unique constraint "users_email_key"`, "permission denied for table comments", "nope", "", "", ""},
		"prod":   {`duplicate key value violates unique constraint "users_email_key"`, "permission denied", "database error", "invalid operation", "invalid geometry", `syntax error in tsquery: "a &&"`},
		"strict": {"constraint violation", "permission denied", "database error", "invalid operation", "invalid input value", "invalid input value"},
	} {
		t.Run(level, func(t *testing.T) {
			srv := testServer(t, func(c *config.Config) {
				c.Errors.Detail = level
				c.RLS.Enabled = true
				c.RLS.Auth.Mode = "headers"
			}).URL
			pgObject := func(t *testing.T, _ *http.Response, p problem) {
				t.Helper()
				if (p.PG != nil) != (level == "dev") {
					t.Errorf("%s: pg object = %v", level, p.PG)
				}
				if level == "dev" && (p.PG["constraint"] != "users_email_key" || p.PG["table"] != "users" || p.PG["sqlstate"] != "23505") {
					t.Errorf("dev pg object = %v", p.PG)
				}
			}
			rows := []errorRow{
				{name: "23505", method: dup.method, path: dup.path, body: dup.body, hdr: dup.hdr, status: dup.status, code: dup.code, check: func(t *testing.T, res *http.Response, p problem) {
					detailIs(want.dup)(t, res, p)
					pgObject(t, res, p)
				}},
				{name: "42501", path: perm.path, status: perm.status, code: perm.code, check: detailIs(want.perm)},
				{name: "P0001", path: raise.path, status: raise.status, code: raise.code, check: detailIs(want.raise)},
			}
			if want.syntax != "" {
				rows = append(rows, errorRow{name: "42601", path: syntax.path, status: syntax.status, code: syntax.code, check: detailIs(want.syntax)},
					errorRow{name: "postgis input", path: geom.path, status: geom.status, code: geom.code, check: detailIs(want.geom)},
					errorRow{name: "contrib input", path: tsq.path, status: tsq.status, code: tsq.code, check: detailIs(want.tsq)})
			} else {
				rows = append(rows, errorRow{name: "postgis input", path: geom.path, status: geom.status, code: geom.code, check: func(t *testing.T, _ *http.Response, p problem) {
					if !strings.Contains(p.Detail, "mixed SRID") {
						t.Errorf("dev postgis detail = %q", p.Detail)
					}
				}})
			}
			// Grammar errors are always rendered in full.
			rows = append(rows, errorRow{name: "grammar", path: "/api/users?select=emial", status: 400, code: "unknown_column", check: func(t *testing.T, _ *http.Response, p problem) {
				if !strings.Contains(p.Detail, `"emial"`) {
					t.Errorf("grammar detail sanitized: %q", p.Detail)
				}
			}})
			runRows(t, srv, rows)
		})
	}
}
