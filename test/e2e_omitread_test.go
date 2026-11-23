package e2e

import (
	"context"
	"net/http"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbr/internal/config"
)

func omitReadFixture(t *testing.T) {
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
			`DROP TABLE IF EXISTS public.or_notes`,
			`DROP TABLE IF EXISTS public.or_staff`,
		} {
			if _, err := conn.Exec(ctx, ddl); err != nil {
				t.Errorf("omit read fixture cleanup: %v", err)
			}
		}
	}
	drop()
	t.Cleanup(func() {
		drop()
		conn.Close(ctx)
	})
	for _, ddl := range []string{
		`CREATE TABLE public.or_staff (
		    id     integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		    name   text NOT NULL,
		    badge  text UNIQUE,
		    salary integer NOT NULL,
		    meta   jsonb)`,
		`COMMENT ON COLUMN public.or_staff.badge IS '@omit read'`,
		`COMMENT ON COLUMN public.or_staff.salary IS '@omit read'`,
		`COMMENT ON COLUMN public.or_staff.meta IS '@omit read'`,
		`INSERT INTO public.or_staff (name, badge, salary, meta) VALUES
		    ('a', 'b-1', 60000, '{"k": 1}'), ('b', 'b-2', 40000, '{"k": 2}')`,
		`CREATE TABLE public.or_notes (
		    id    integer PRIMARY KEY,
		    staff integer REFERENCES public.or_staff (id),
		    body  text)`,
		`CREATE INDEX or_notes_body_idx ON public.or_notes (body)`,
		`COMMENT ON COLUMN public.or_notes.id IS '@omit read'`,
		`INSERT INTO public.or_notes VALUES (1, 1, 'x'), (2, 2, 'y'), (3, 2, 'z')`,
	} {
		if _, err := conn.Exec(ctx, ddl); err != nil {
			t.Fatalf("omit read fixture: %v\n%s", err, ddl)
		}
	}
}

func TestOmitReadHidesColumnEverywhere(t *testing.T) {
	omitReadFixture(t)
	srv := testServer(t, func(c *config.Config) { c.Aggregates.Enabled = true }).URL
	var one row
	getJSON(t, srv+"/api/or_staff/1", &one)
	if len(one) != 2 || one["name"] != "a" {
		t.Fatalf("row = %v, want id and name only", one)
	}
	for _, target := range []string{
		"/api/or_staff?salary=gt.50000",
		"/api/or_staff?salary=not.lt.50000",
		"/api/or_staff?or=(salary.gt.50000,id.eq.2)",
		"/api/or_staff?and=(id.gt.0,or(salary.lt.1,id.eq.2))",
		"/api/or_staff?not.or=(salary.gt.1,id.eq.1)",
		"/api/or_staff?meta->>k=eq.1",
		"/api/or_staff?meta->k=gt.0",
		"/api/or_staff?badge=eq.b-1",
		"/api/or_staff?order=salary.desc",
		"/api/or_staff?order=id.asc,salary.desc",
		"/api/or_staff?distinct=salary",
		"/api/or_staff?select=salary.sum()",
		"/api/or_staff?select=salary.avg()",
		"/api/or_staff?select=salary,count()",
		"/api/or_staff?select=count()&salary=gt.1",
		"/api/or_staff?select=id,...or_notes(body)&salary=gt.1",
		"/api/or_notes?select=body,or_staff(name)&or_staff.salary=gt.50000",
		"/api/or_notes?select=body,or_staff(name)&or_staff.order=salary.asc",
		"/api/or_notes?or_staff.salary=gt.50000",
		"/api/or_notes?order=or_staff(salary).asc",
		"/api/or_notes?select=body,...or_staff(salary)",
	} {
		res, body := do(t, "GET", srv+target, nil)
		wantProblem(t, res, body, http.StatusBadRequest, "unknown_column")
	}
	for _, target := range []string{
		"/api/or_staff?salary=gt.50000",
		"/api/or_staff?or=(salary.gt.50000,id.eq.2)",
	} {
		for _, m := range []string{"PATCH", "DELETE"} {
			res, body := send(t, m, srv+target, `{"name":"z"}`, nil)
			wantProblem(t, res, body, http.StatusBadRequest, "unknown_column")
		}
	}
}

func TestOmitReadUniqueKeys(t *testing.T) {
	omitReadFixture(t)
	srv := testServer(t, nil).URL
	for _, m := range []string{"GET", "PATCH", "DELETE", "PUT"} {
		res, body := send(t, m, srv+"/api/or_staff/by/badge/b-1", `{"name":"z"}`, nil)
		wantProblem(t, res, body, http.StatusNotFound, "not_found")
	}
	for _, tc := range []struct{ method, query string }{
		{"PUT", "?on_conflict=badge"},
		{"POST", "?on_conflict=badge"},
	} {
		hdr := map[string]string{"Prefer": "resolution=merge-duplicates"}
		res, body := send(t, tc.method, srv+"/api/or_staff"+tc.query, `{"name":"z","badge":"b-1","salary":1}`, hdr)
		wantProblem(t, res, body, http.StatusBadRequest, "unknown_column")
	}
}

func TestOmitReadPrimaryKey(t *testing.T) {
	omitReadFixture(t)
	srv := testServer(t, nil).URL
	res, body := do(t, "GET", srv+"/api/or_notes?order=body.asc&limit=1", nil)
	if res.StatusCode != http.StatusOK || res.Header.Get("X-Next-Cursor") != "" {
		t.Fatalf("page = %d cursor %q %s", res.StatusCode, res.Header.Get("X-Next-Cursor"), body)
	}
	for _, p := range []string{"after", "before"} {
		res, body = do(t, "GET", srv+"/api/or_notes?"+p+"=WyJwdWJsaWMub3Jfbm90ZXMiLDFd", nil)
		wantProblem(t, res, body, http.StatusBadRequest, "keyset_unsupported")
	}
	for _, m := range []string{"GET", "PATCH", "DELETE", "PUT"} {
		res, body = send(t, m, srv+"/api/or_notes/1", `{"body":"q"}`, nil)
		wantProblem(t, res, body, http.StatusNotFound, "not_found")
	}
	res, body = send(t, "POST", srv+"/api/or_notes", `{"id":9,"body":"n"}`, nil)
	if res.StatusCode != http.StatusCreated || res.Header.Get("Location") != "" {
		t.Fatalf("POST = %d Location %q %s", res.StatusCode, res.Header.Get("Location"), body)
	}
}
