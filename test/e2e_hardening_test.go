package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbr/internal/config"
)

// hdFixture creates the hd_ objects and drops them at the end of the test:
// hd_a (2 rows) ← hd_b (3 per hd_a) ← hd_c (3 per hd_b), the computed column
// hd_b.boom that raises when it runs, the SETOF hd_a function hd_a_all, the
// CSV probe hd_csv and hd_tilde with ~-prefixed columns.
func hdFixture(t *testing.T) {
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
			`DROP FUNCTION IF EXISTS public.hd_a_all()`,
			`DROP FUNCTION IF EXISTS public.hd_b_boom(public.hd_b)`,
			`DROP TABLE IF EXISTS public.hd_c, public.hd_b, public.hd_a, public.hd_csv, public.hd_tilde`,
		} {
			if _, err := conn.Exec(ctx, ddl); err != nil {
				t.Errorf("hd fixture cleanup: %v", err)
			}
		}
	}
	drop()
	t.Cleanup(func() {
		drop()
		conn.Close(ctx)
	})
	for _, ddl := range []string{
		`CREATE TABLE public.hd_a (id integer PRIMARY KEY)`,
		`CREATE TABLE public.hd_b (id integer PRIMARY KEY, a_id integer NOT NULL REFERENCES public.hd_a)`,
		`CREATE INDEX ON public.hd_b (a_id)`,
		`CREATE TABLE public.hd_c (id integer PRIMARY KEY, b_id integer NOT NULL REFERENCES public.hd_b)`,
		`CREATE INDEX ON public.hd_c (b_id)`,
		`INSERT INTO public.hd_a SELECT g FROM generate_series(1, 2) g`,
		`INSERT INTO public.hd_b SELECT g, (g - 1) / 3 + 1 FROM generate_series(1, 6) g`,
		`INSERT INTO public.hd_c SELECT g, (g - 1) / 3 + 1 FROM generate_series(1, 18) g`,
		`CREATE FUNCTION public.hd_b_boom(b public.hd_b) RETURNS integer STABLE LANGUAGE plpgsql AS
		    $$ BEGIN RAISE EXCEPTION 'hd_b_boom ran for %', b.id; END $$`,
		`CREATE FUNCTION public.hd_a_all() RETURNS SETOF public.hd_a STABLE LANGUAGE sql AS $$ SELECT * FROM public.hd_a $$`,
		`CREATE TABLE public.hd_csv (id integer PRIMARY KEY, v text)`,
		`INSERT INTO public.hd_csv VALUES (6, E'\r=1'), (7, E'a\rb'), (8, E'line1\nline2'), (9, E'crlf\r\nend')`,
		`CREATE TABLE public.hd_tilde (id integer PRIMARY KEY, "~x" text, "~j" jsonb DEFAULT '{"a": 1}', "~u" text UNIQUE)`,
		`INSERT INTO public.hd_tilde (id, "~x", "~u") VALUES (1, 'tx', 'u1'), (2, 'ty', 'u2')`,
	} {
		if _, err := conn.Exec(ctx, ddl); err != nil {
			t.Fatalf("hd fixture: %v\n%s", err, ddl)
		}
	}
}

// TestLimitZeroIsCountOnly: limit=0 projects no row, so neither an embed
// nor a computed column runs (hd_b.boom raises whenever it does), and the
// estimate stays what the statement reads, while limit=1 with the same
// selection is priced and executed in full.
func TestLimitZeroIsCountOnly(t *testing.T) {
	hdFixture(t)
	srv := testServer(t, func(c *config.Config) { c.Limits.MaxCost = 50 }).URL

	res, body := get(t, srv+"/api/hd_a?select=id,hd_b(id,hd_c(id))&limit=1")
	wantProblem(t, res, body, http.StatusBadRequest, "cost_exceeded")
	res, body = get(t, srv+"/api/hd_a?select=id,hd_b(id,boom)&hd_b.limit=1&limit=1")
	if res.StatusCode == http.StatusOK || !strings.Contains(string(body), "hd_b_boom ran") {
		t.Fatalf("limit=1 did not run the computed column: %d %s", res.StatusCode, body)
	}

	for _, path := range []string{
		"/api/hd_a?select=id,hd_b(id,hd_c(id))&limit=0&count=true",
		"/api/hd_a?select=id,hd_b(id,boom,hd_c(id))&limit=0&count=true",
		"/api/hd_a?select=id,...hd_b(boom,hd_c(id))&limit=0&count=true",
		"/api/hd_a?select=id,hd_b(boom)&limit=0&offset=1&count=true",
		"/api/rpc/hd_a_all?select=id,hd_b(id,boom,hd_c(id))&limit=0&count=true",
	} {
		res, body := get(t, srv+path)
		if res.StatusCode != http.StatusPartialContent || strings.TrimSpace(string(body)) != "[]" ||
			res.Header.Get("X-Total-Count") != "2" || res.Header.Get("Link") != "" {
			t.Errorf("%s = %d %s %v", path, res.StatusCode, body, res.Header)
		}
	}
	res, body = get(t, srv+"/api/hd_a?select=id,hd_b(nope)&limit=0")
	wantProblem(t, res, body, http.StatusBadRequest, "unknown_column")
}

// TestCSVKeepsLineBreaks: carriage returns and line feeds inside values
// come out byte for byte (quoted), records end in CRLF, and the formula
// guard still applies to a value starting with a carriage return.
func TestCSVKeepsLineBreaks(t *testing.T) {
	hdFixture(t)
	srv := testServer(t, nil).URL
	res, body := do(t, "GET", srv+"/api/hd_csv?order=id", map[string]string{"Accept": "text/csv"})
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != csvType {
		t.Fatalf("csv = %d %q %s", res.StatusCode, res.Header.Get("Content-Type"), body)
	}
	want := "id,v\r\n" + "6,\"'\r=1\"\r\n" + "7,\"a\rb\"\r\n" + "8,\"line1\nline2\"\r\n" + "9,\"crlf\r\nend\"\r\n"
	if string(body) != want {
		t.Errorf("csv =\n%q\nwant\n%q", body, want)
	}
	plain := testServer(t, func(c *config.Config) { c.Server.CSVEscapeFormulas = false }).URL
	_, body = do(t, "GET", plain+"/api/hd_csv?id=eq.6", map[string]string{"Accept": "text/csv"})
	if string(body) != "id,v\r\n6,\"\r=1\"\r\n" {
		t.Errorf("unescaped csv = %q", body)
	}
}

// TestReservedColumnsUnreachable: a column named ~x is returned by
// select=* and written by a body, but no query parameter names it.
func TestReservedColumnsUnreachable(t *testing.T) {
	hdFixture(t)
	srv := testServer(t, func(c *config.Config) {
		c.Filters.IndexedOnly = false
		c.Aggregates.Enabled = true
	}).URL

	var rows []row
	if res := getJSON(t, srv+"/api/hd_tilde?order=id", &rows); res.StatusCode != http.StatusOK || len(rows) != 2 || rows[0]["~x"] != "tx" {
		t.Fatalf("select=* = %d %v", res.StatusCode, rows)
	}
	for _, path := range []string{
		"/api/hd_tilde?select=~x",
		"/api/hd_tilde?select=~x::text",
		"/api/hd_tilde?select=~x.max()",
		"/api/hd_tilde?select=~x.count()",
		"/api/hd_tilde?select=~j->a",
		"/api/hd_tilde?select=~j->>a::int",
		"/api/hd_tilde?~x=eq.tx",
		"/api/hd_tilde?~j->>a=eq.1",
		"/api/hd_tilde?order=~x.desc",
		"/api/hd_tilde?order=~j->a&select=id",
		"/api/hd_tilde?distinct=~x&select=id",
	} {
		res, body := get(t, srv+path)
		var p problem
		if res.StatusCode != http.StatusBadRequest || json.Unmarshal(body, &p) != nil || p.Code != "unknown_column" {
			t.Errorf("%s = %d %s, want 400 unknown_column", path, res.StatusCode, body)
		}
	}
	res, body := send(t, "POST", srv+"/api/hd_tilde?on_conflict=~u", `{"id": 3, "~u": "u3"}`,
		map[string]string{"Prefer": "resolution=merge-duplicates"})
	wantProblem(t, res, body, http.StatusBadRequest, "unknown_column")
	res, body = send(t, "POST", srv+"/api/hd_tilde?columns=id,~x", `{"id": 3, "~x": "tz"}`, nil)
	wantProblem(t, res, body, http.StatusBadRequest, "invalid_body")
	res, body = send(t, "POST", srv+"/api/hd_tilde", `{"id": 3, "~x": "tz"}`, map[string]string{"Prefer": "return=representation"})
	if res.StatusCode != http.StatusCreated || !strings.Contains(string(body), `"tz"`) {
		t.Errorf("body write of ~x = %d %s", res.StatusCode, body)
	}
}

// TestVaryOnPreflightAndAuthErrors: CORS preflight answers vary on Origin
// and 401/403 answers on Accept, like every other API response.
func TestVaryOnPreflightAndAuthErrors(t *testing.T) {
	srv := testServer(t, func(c *config.Config) {
		c.Server.CORSOrigins = []string{"https://app.example.com"}
		jwtAuth(c, "e2e-vary-secret-0123456789abcdef0123")
	}).URL
	for _, origin := range []string{"https://app.example.com", "https://evil.example.com"} {
		res, _ := do(t, "OPTIONS", srv+"/api/users", map[string]string{"Origin": origin, "Access-Control-Request-Method": "GET"})
		if vary := strings.Join(res.Header.Values("Vary"), ", "); res.StatusCode != http.StatusNoContent || !strings.Contains(vary, "Origin") {
			t.Errorf("preflight from %s = %d, Vary %q", origin, res.StatusCode, vary)
		}
	}
	res, body := do(t, "GET", srv+"/api/users", map[string]string{"Authorization": "Bearer garbage"})
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token = %d %s", res.StatusCode, body)
	}
	if got := res.Header.Values("Vary"); !reflect.DeepEqual(got, []string{"Origin", "Accept"}) {
		t.Errorf("401 Vary = %q, want Origin and Accept", got)
	}
}
