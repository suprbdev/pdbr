package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pdbcore/pgexec"
	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/exec"
	"github.com/suprbdev/pdbr/internal/inflect"
)

// scriptedDB answers QueryRow from a script keyed by a substring of the SQL
// (first match wins) and records what it saw. It never begins transactions.
type scriptedDB struct {
	answers []struct {
		match string
		data  []byte
		err   error
	}
	seen []string
}

type scriptedRow struct {
	data []byte
	err  error
}

func (r scriptedRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*(dest[0].(*[]byte)) = r.data
	return nil
}

func (s *scriptedDB) on(match string, data string, err error) *scriptedDB {
	s.answers = append(s.answers, struct {
		match string
		data  []byte
		err   error
	}{match, []byte(data), err})
	return s
}

func (s *scriptedDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("not used")
}

func (s *scriptedDB) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	s.seen = append(s.seen, sql)
	for _, a := range s.answers {
		if strings.Contains(sql, a.match) {
			return scriptedRow{data: a.data, err: a.err}
		}
	}
	return scriptedRow{err: pgx.ErrNoRows}
}

func (s *scriptedDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (s *scriptedDB) Acquire(context.Context) (*pgxpool.Conn, error) {
	return nil, errors.New("scriptedDB never begins transactions")
}

func handlerServer(t *testing.T, db exec.DB, mutate func(*exec.Options)) (*httptest.Server, *Handler) {
	t.Helper()
	cfg := serverCfg()
	built, err := api.New(testutil.FixtureCatalog(), inflect.Default, api.Options{
		FilterIndexedOnly: true, Functions: true, BasePath: cfg.BasePath, Schemas: []string{"public"},
		MaxPageSize: 100, Etags: true, Version: "test", Logger: quiet(),
	}).Build()
	if err != nil {
		t.Fatal(err)
	}
	opts := exec.Options{BasePath: cfg.BasePath, Logger: quiet(), MaxPageSize: 100, MaxEmbedDepth: 5, MaxCost: 10000}
	if mutate != nil {
		mutate(&opts)
	}
	ex := exec.New(db, built, compile.New(built).Compile, nil, opts)
	h := NewHandler(built, ex, ParseOptions(built, 5, 100, true, true), cfg.BasePath, quiet())
	rt, err := NewRuntime(built, h)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(cfg, nil, rt, quiet()).Handler())
	t.Cleanup(srv.Close)
	return srv, h
}

const pageJSON = `{"rows":[{"id":1,"email":"a@b.c"},{"id":2,"email":"d@e.f"}],"has_next":true,"has_prev":false,"first":[1],"last":[2],"total":7}`

func TestHandlerList(t *testing.T) {
	db := (&scriptedDB{}).on(`FROM "public"."users"`, pageJSON, nil)
	srv, _ := handlerServer(t, db, nil)
	res, body := do(t, "GET", srv.URL+"/api/users?select=id,email&limit=2&count=true", nil)
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != ContentTypeJSON {
		t.Fatalf("status=%d ct=%q body=%s", res.StatusCode, res.Header.Get("Content-Type"), body)
	}
	if body != `[{"id":1,"email":"a@b.c"},{"id":2,"email":"d@e.f"}]` {
		t.Fatalf("body = %s", body)
	}
	if res.Header.Get("X-Total-Count") != "7" || res.Header.Get("X-Next-Cursor") == "" || res.Header.Get("X-Prev-Cursor") != "" {
		t.Fatalf("headers = %v", res.Header)
	}
	if link := res.Header.Get("Link"); !strings.HasPrefix(link, `</api/users?select=id,email&limit=2&count=true&after=`) || !strings.HasSuffix(link, `>; rel="next"`) {
		t.Fatalf("Link = %q", link)
	}
	if !strings.Contains(db.seen[0], `LIMIT $`) {
		t.Fatalf("sql = %s", db.seen[0])
	}
}

func TestHandlerHeadParity(t *testing.T) {
	db := (&scriptedDB{}).on(`FROM "public"."users"`, pageJSON, nil)
	srv, _ := handlerServer(t, db, nil)
	get, body := do(t, "GET", srv.URL+"/api/users?count=true", nil)
	head, hbody := do(t, "HEAD", srv.URL+"/api/users?count=true", nil)
	if head.StatusCode != 200 || hbody != "" || body == "" {
		t.Fatalf("head=%d %q get body=%q", head.StatusCode, hbody, body)
	}
	for _, h := range []string{"X-Total-Count", "X-Next-Cursor", "Link", "Content-Type"} {
		if get.Header.Get(h) != head.Header.Get(h) {
			t.Errorf("%s: GET %q != HEAD %q", h, get.Header.Get(h), head.Header.Get(h))
		}
	}
	if len(db.seen) != 2 || db.seen[0] != db.seen[1] {
		t.Fatalf("HEAD must compile identically to GET: %d statements", len(db.seen))
	}
}

func TestHandlerRowAndEtag(t *testing.T) {
	db := (&scriptedDB{}).on(`WHERE t0."id" = $1`, `{"etag":"9e10","row":{"id":1,"email":"a@b.c"}}`, nil)
	srv, _ := handlerServer(t, db, nil)
	res, body := do(t, "GET", srv.URL+"/api/users/1?select=id,email", nil)
	if res.StatusCode != 200 || res.Header.Get("ETag") != `W/"9e10"` || body != `{"id":1,"email":"a@b.c"}` {
		t.Fatalf("status=%d etag=%q body=%s", res.StatusCode, res.Header.Get("ETag"), body)
	}
	res, body = do(t, "GET", srv.URL+"/api/users/1", map[string]string{"If-None-Match": `W/"9e10"`})
	if res.StatusCode != 304 || body != "" || res.Header.Get("ETag") != `W/"9e10"` {
		t.Fatalf("304: status=%d body=%q etag=%q", res.StatusCode, body, res.Header.Get("ETag"))
	}
	res, body = do(t, "GET", srv.URL+"/api/users/1", map[string]string{"If-None-Match": `W/"stale"`})
	if res.StatusCode != 200 {
		t.Fatalf("stale: status=%d body=%s", res.StatusCode, body)
	}
}

func TestHandlerNotFoundRow(t *testing.T) {
	srv, _ := handlerServer(t, &scriptedDB{}, nil)
	res, body := do(t, "GET", srv.URL+"/api/users/999", nil)
	p := problem(t, body)
	if res.StatusCode != 404 || p.Code != CodeNotFound || p.Instance != "/api/users/999" || res.Header.Get("Content-Type") != ContentTypeProblem {
		t.Fatalf("status=%d problem=%+v", res.StatusCode, p)
	}
	res, body = do(t, "HEAD", srv.URL+"/api/users/999", nil)
	if res.StatusCode != 404 || body != "" {
		t.Fatalf("HEAD 404: status=%d body=%q", res.StatusCode, body)
	}
}

func TestHandlerParseErrors(t *testing.T) {
	srv, _ := handlerServer(t, &scriptedDB{}, nil)
	cases := map[string]struct {
		status int
		code   string
	}{
		"/api/users?select=nope":                      {400, "unknown_column"},
		"/api/users?full_name=eq.x":                   {400, "column_not_filterable"},
		"/api/users?limit=1000":                       {400, "page_size_exceeded"},
		"/api/metrics?after=WyJwdWJsaWMucG9zdHMiLDJd": {400, "keyset_unsupported"},
		"/api/users?after=nope":                       {400, "invalid_cursor"},
		"/api/comments?select=comments(id)":           {400, "ambiguous_relation"},
	}
	for u, want := range cases {
		res, body := do(t, "GET", srv.URL+u, nil)
		p := problem(t, body)
		if res.StatusCode != want.status || p.Code != want.code {
			t.Errorf("%s: got %d %s (%s), want %d %s", u, res.StatusCode, p.Code, p.Detail, want.status, want.code)
		}
	}
	res, body := do(t, "GET", srv.URL+"/api/users", map[string]string{"Accept": "application/xml"})
	if p := problem(t, body); res.StatusCode != 406 || p.Code != "not_acceptable" {
		t.Fatalf("406: %d %+v", res.StatusCode, p)
	}
}

func TestHandlerCSV(t *testing.T) {
	db := (&scriptedDB{}).on(`FROM "public"."users"`, pageJSON, nil)
	srv, _ := handlerServer(t, db, nil)
	res, body := do(t, "GET", srv.URL+"/api/users?select=id,email&limit=2", map[string]string{"Accept": "text/csv"})
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != ContentTypeCSV || body != "id,email\r\n1,a@b.c\r\n2,d@e.f\r\n" {
		t.Fatalf("csv: %d %q %q", res.StatusCode, res.Header.Get("Content-Type"), body)
	}
	if res.Header.Get("Link") == "" {
		t.Errorf("csv response lost the page headers: %v", res.Header)
	}
	head, hb := do(t, "HEAD", srv.URL+"/api/users?select=id,email&limit=2", map[string]string{"Accept": "text/csv"})
	if head.StatusCode != 200 || head.Header.Get("Content-Type") != ContentTypeCSV || len(hb) != 0 || head.Header.Get("Link") != res.Header.Get("Link") {
		t.Fatalf("HEAD csv: %d %v %q", head.StatusCode, head.Header, hb)
	}
	res, body = do(t, "GET", srv.URL+"/api/users?select=id,posts(id)", map[string]string{"Accept": "text/csv"})
	if p := problem(t, body); res.StatusCode != 406 || p.Code != "not_acceptable" {
		t.Fatalf("csv with embeds: %d %+v", res.StatusCode, p)
	}
}

func TestHandlerDatabaseErrors(t *testing.T) {
	db := (&scriptedDB{}).on(`FROM "public"."users"`, "", &pgconn.PgError{Code: "42501", Message: "permission denied for table users"})
	srv, _ := handlerServer(t, db, func(o *exec.Options) { o.Detail = pgexec.Dev })
	res, body := do(t, "GET", srv.URL+"/api/users", nil)
	p := problem(t, body)
	if res.StatusCode != 403 || p.Code != "pg:42501" || p.PG["sqlstate"] != "42501" || p.Detail != "permission denied for table users" {
		t.Fatalf("got %d %+v", res.StatusCode, p)
	}
	db = (&scriptedDB{}).on(`FROM "public"."users"`, "", &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"})
	srv, _ = handlerServer(t, db, nil)
	res, body = do(t, "GET", srv.URL+"/api/users", nil)
	if p = problem(t, body); res.StatusCode != 504 || p.Code != "pg:57014" || p.Detail != "database error" || p.PG != nil {
		t.Fatalf("got %d %+v", res.StatusCode, p)
	}
}

func TestHandlerReadOnly(t *testing.T) {
	srv, _ := handlerServer(t, &scriptedDB{}, func(o *exec.Options) {
		o.ReadOnly = new(atomic.Bool)
		o.ReadOnly.Store(true)
	})
	res, body := do(t, "POST", srv.URL+"/api/users", nil)
	p := problem(t, body)
	if res.StatusCode != 405 || p.Code != exec.CodeReadOnly || res.Header.Get("Allow") != "GET, HEAD" {
		t.Fatalf("got %d %+v allow=%q", res.StatusCode, p, res.Header.Get("Allow"))
	}
}

func xerr(err error) *exec.Error {
	var xe *exec.Error
	if !errors.As(err, &xe) {
		return &exec.Error{}
	}
	return xe
}

func TestHandlerCompileSQL(t *testing.T) {
	_, h := handlerServer(t, &scriptedDB{}, nil)
	r, _ := http.NewRequest("GET", "http://x/api/users?select=id&limit=2", nil)
	out, err := h.CompileSQL(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "-- request: GET /api/users?select=id&limit=2\n") || !strings.Contains(out, `FROM "public"."users"`) || !strings.HasSuffix(out, "-- args: [3,2]\n") {
		t.Fatalf("out = %q", out)
	}
	r, _ = http.NewRequest("GET", "http://x/api/nope", nil)
	if _, err = h.CompileSQL(r); xerr(err).Status != 404 {
		t.Fatalf("404: %v", err)
	}
	r, _ = http.NewRequest("POST", "http://x/api/metrics", nil)
	if _, err = h.CompileSQL(r); xerr(err).Status != 405 || xerr(err).Allow != "GET, HEAD" {
		t.Fatalf("405: %v", err)
	}
	r, _ = http.NewRequest("GET", "http://x/api/users?select=nope", nil)
	if _, err = h.CompileSQL(r); xerr(err).Code != "unknown_column" {
		t.Fatalf("400: %v", err)
	}
}
