package exec

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/suprbdev/pdbcore/pgexec"
	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/inflect"
	"github.com/suprbdev/pdbr/internal/plan"
)

func built(t testing.TB) *api.Built {
	t.Helper()
	b := api.New(testutil.FixtureCatalog(), inflect.Default, api.Options{
		FilterIndexedOnly: true, Functions: true, BasePath: "/api", Schemas: []string{"public"},
		MaxPageSize: 100, Etags: true, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	bt, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	return bt
}

// ---- fakes ----

type fakeRow struct {
	data []byte
	err  error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*(dest[0].(*[]byte)) = r.data
	return nil
}

// fakeDB answers every QueryRow with the configured row and records the
// SQL it saw; BeginTx hands out a fakeTx that records identity statements.
type fakeDB struct {
	row     fakeRow
	queries []string
	txs     []*fakeTx
}

func (f *fakeDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("not used")
}

func (f *fakeDB) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	f.queries = append(f.queries, sql)
	return f.row
}

func (f *fakeDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (f *fakeDB) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	tx := &fakeTx{db: f}
	f.txs = append(f.txs, tx)
	return tx, nil
}

type fakeTx struct {
	pgx.Tx
	db        *fakeDB
	execs     []string
	committed bool
	rolled    bool
}

func (t *fakeTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	t.execs = append(t.execs, sql)
	return pgconn.CommandTag{}, nil
}

func (t *fakeTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return t.db.QueryRow(ctx, sql, args...)
}

func (t *fakeTx) Commit(context.Context) error   { t.committed = true; return nil }
func (t *fakeTx) Rollback(context.Context) error { t.rolled = true; return nil }

type recHook struct {
	before, after int
	err           error
	forceTx       bool
}

func (h *recHook) BeforeRequest(ctx context.Context, op *Operation) (context.Context, error) {
	h.before++
	if h.forceTx {
		op.ForceTx = true
	}
	return ctx, h.err
}

func (h *recHook) AfterRequest(_ context.Context, _ *Operation, _ *Result) { h.after++ }

func listOp(b *api.Built, path string) *Operation {
	r := b.ByPath[strings.TrimPrefix(path, "/")]
	return &Operation{Method: "GET", Path: path, Resource: r,
		Plan: &plan.Request{Method: plan.GET, Resource: r, Page: plan.Page{Limit: 100}}}
}

func newExec(db DB, b *api.Built, hooks []RequestHook, opts Options) *Executor {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return New(db, b, compile.New(b).Compile, hooks, opts)
}

func asErr(err error) *Error {
	var xe *Error
	if !errors.As(err, &xe) {
		return &Error{}
	}
	return xe
}

func errOf(t *testing.T, res *Result) *Error {
	t.Helper()
	var xe *Error
	if res.Err == nil || !errors.As(res.Err, &xe) {
		t.Fatalf("expected *Error, got %v (status %d)", res.Err, res.Status)
	}
	return xe
}

// ---- gates and hooks ----

func TestExecuteReadOnlyGate(t *testing.T) {
	b := built(t)
	var ro atomic.Bool
	ro.Store(true)
	db := &fakeDB{}
	h := &recHook{}
	e := newExec(db, b, []RequestHook{h}, Options{ReadOnly: &ro})
	op := listOp(b, "/users")
	op.Method, op.Mutation = "POST", true
	res := e.Execute(context.Background(), op)
	if xe := errOf(t, res); xe.Status != http.StatusMethodNotAllowed || xe.Code != CodeReadOnly {
		t.Fatalf("got %+v", xe)
	}
	if len(db.queries) != 0 || h.before != 1 || h.after != 1 {
		t.Fatalf("queries=%d before=%d after=%d", len(db.queries), h.before, h.after)
	}
	ro.Store(false)
	op = listOp(b, "/users")
	db.row = fakeRow{data: []byte(`{"rows":[],"has_next":false,"has_prev":false}`)}
	if res := e.Execute(context.Background(), op); res.Err != nil {
		t.Fatalf("read after unset: %v", res.Err)
	}
}

func TestExecuteCostGate(t *testing.T) {
	b := built(t)
	e := newExec(&fakeDB{}, b, nil, Options{MaxCost: 5})
	res := e.Execute(context.Background(), listOp(b, "/users"))
	if xe := errOf(t, res); xe.Status != 400 || xe.Code != compile.CodeCostExceeded {
		t.Fatalf("got %+v", xe)
	}
}

func TestExecuteHookErrors(t *testing.T) {
	b := built(t)
	e := newExec(&fakeDB{}, b, []RequestHook{&recHook{err: compile.NewUserError("invalid_body", "nope")}}, Options{})
	xe := errOf(t, e.Execute(context.Background(), listOp(b, "/users")))
	if xe.Status != 400 || xe.Code != "invalid_body" || xe.Detail != "nope" {
		t.Fatalf("user error: %+v", xe)
	}
	e = newExec(&fakeDB{}, b, []RequestHook{&recHook{err: errors.New("boom")}}, Options{})
	xe = errOf(t, e.Execute(context.Background(), listOp(b, "/users")))
	if xe.Status != 500 || xe.Code != CodeInternal || xe.Detail != "internal error" {
		t.Fatalf("internal in prod: %+v", xe)
	}
	e = newExec(&fakeDB{}, b, []RequestHook{&recHook{err: errors.New("boom")}}, Options{Detail: pgexec.Dev})
	if xe = errOf(t, e.Execute(context.Background(), listOp(b, "/users"))); xe.Detail != "boom" {
		t.Fatalf("internal in dev: %+v", xe)
	}
}

func TestExecuteCompileErrors(t *testing.T) {
	b := built(t)
	broken := func(context.Context, *compile.Request) (*compile.Statement, error) {
		return nil, errors.New("bad plan")
	}
	e := New(&fakeDB{}, b, broken, nil, Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	xe := errOf(t, e.Execute(context.Background(), listOp(b, "/users")))
	if xe.Status != 500 || xe.Detail != "compile: invalid request" {
		t.Fatalf("got %+v", xe)
	}
	notImpl := func(context.Context, *compile.Request) (*compile.Statement, error) {
		return nil, compile.ErrNotImplemented
	}
	e = New(&fakeDB{}, b, notImpl, nil, Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if xe = errOf(t, e.Execute(context.Background(), listOp(b, "/users"))); xe.Status != 501 || xe.Code != compile.CodeNotImplemented {
		t.Fatalf("got %+v", xe)
	}
}

func TestPrepareDoesNotTouchDB(t *testing.T) {
	b := built(t)
	e := newExec(nil, b, nil, Options{})
	stmt, err := e.Prepare(context.Background(), listOp(b, "/users"))
	if err != nil || stmt == nil || stmt.Shape != compile.ShapePage {
		t.Fatalf("stmt=%v err=%v", stmt, err)
	}
	if res := e.Execute(context.Background(), listOp(b, "/users")); res.Status != http.StatusServiceUnavailable {
		t.Fatalf("nil db should be 503, got %d", res.Status)
	}
}

// ---- transaction policy ----

func TestTransactionDecision(t *testing.T) {
	b := built(t)
	page := []byte(`{"rows":[{"id":1}],"has_next":false,"has_prev":false,"first":[1],"last":[1]}`)
	cases := []struct {
		name   string
		opts   Options
		role   string
		claims map[string]any
		mut    bool
		force  bool
		wantTx bool
	}{
		{"read no identity", Options{RLS: true}, "", nil, false, false, false},
		{"read with role", Options{RLS: true}, "anonymous", nil, false, false, true},
		{"read with claims", Options{RLS: true}, "", map[string]any{"user_id": 1}, false, false, true},
		{"read with role, rls off", Options{RLS: false}, "anonymous", nil, false, false, false},
		{"read forced", Options{RLS: true}, "", nil, false, true, true},
		{"mutation tx policy", Options{TxMutations: true}, "", nil, true, false, true},
		{"mutation no tx policy", Options{TxMutations: false}, "", nil, true, false, false},
		{"mutation no policy but claims", Options{TxMutations: false, RLS: true}, "app_user", nil, true, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := &fakeDB{row: fakeRow{data: page}}
			hooks := []RequestHook{&recHook{forceTx: tc.force}}
			e := newExec(db, b, hooks, tc.opts)
			op := listOp(b, "/users")
			op.Role, op.Claims, op.Mutation = tc.role, tc.claims, tc.mut
			res := e.Execute(context.Background(), op)
			if res.Err != nil {
				t.Fatalf("err: %v", res.Err)
			}
			if got := len(db.txs) == 1; got != tc.wantTx {
				t.Fatalf("tx = %v, want %v", got, tc.wantTx)
			}
			if tc.wantTx {
				tx := db.txs[0]
				if !tx.committed {
					t.Fatal("not committed")
				}
				execs := strings.Join(tx.execs, ";")
				if tc.role != "" && tc.opts.RLS && !strings.Contains(execs, `SET LOCAL ROLE "`+tc.role+`"`) {
					t.Fatalf("role not applied: %v", tx.execs)
				}
				if len(tc.claims) > 0 && !strings.Contains(execs, "set_config") {
					t.Fatalf("claims not applied: %v", tx.execs)
				}
			}
		})
	}
}

// ---- unwrap ----

func TestUnwrapRow(t *testing.T) {
	b := built(t)
	op := listOp(b, "/users")
	stmt := &compile.Statement{Shape: compile.ShapeRow}
	if _, err := Unwrap(op, stmt, nil, false, "/api"); asErr(err).Status != 404 || asErr(err).Code != CodeNotFound {
		t.Fatalf("missing row: %v", err)
	}
	res, err := Unwrap(op, stmt, []byte(`{"id":1}`), true, "/api")
	if err != nil || res.Status != 200 || string(res.Data) != `{"id":1}` {
		t.Fatalf("row: %v %+v", err, res)
	}
}

func TestUnwrapRowEtag(t *testing.T) {
	b := built(t)
	op := listOp(b, "/users")
	stmt := &compile.Statement{Shape: compile.ShapeRowEtag}
	raw := []byte(`{"etag":"abc","row":{"id":1}}`)
	res, err := Unwrap(op, stmt, raw, true, "/api")
	if err != nil || res.Status != 200 || res.Headers.Get("ETag") != `W/"abc"` || string(res.Data) != `{"id":1}` {
		t.Fatalf("etag: %v %+v", err, res)
	}
	for _, h := range []string{`W/"abc"`, `"abc"`, `*`, `W/"x", W/"abc"`} {
		op.Plan.IfNoneMatch = h
		res, err = Unwrap(op, stmt, raw, true, "/api")
		if err != nil || res.Status != 304 || res.Data != nil || res.Headers.Get("ETag") != `W/"abc"` {
			t.Fatalf("If-None-Match %q: %v %+v", h, err, res)
		}
	}
	op.Plan.IfNoneMatch = `W/"other"`
	if res, _ = Unwrap(op, stmt, raw, true, "/api"); res.Status != 200 {
		t.Fatalf("mismatch should be 200, got %d", res.Status)
	}
	if _, err := Unwrap(op, stmt, nil, false, "/api"); asErr(err).Status != 404 {
		t.Fatalf("missing: %v", err)
	}
}

func TestUnwrapPageKeyset(t *testing.T) {
	b := built(t)
	op := listOp(b, "/posts")
	op.RawQuery = "order=title.asc&limit=2&count=true"
	stmt := &compile.Statement{Shape: compile.ShapePage, Cursors: true, Total: true}
	raw := []byte(`{"rows":[{"id":7},{"id":2}],"has_next":true,"has_prev":false,"first":[7],"last":[2],"total":5}`)
	res, err := Unwrap(op, stmt, raw, true, "/api")
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Data) != `[{"id":7},{"id":2}]` || res.Headers.Get("X-Total-Count") != "5" {
		t.Fatalf("data/total: %s %v", res.Data, res.Headers)
	}
	next := plan.EncodeCursor(op.Resource, []any{json.Number("2")})
	if next != "WyJwdWJsaWMucG9zdHMiLDJd" || res.Headers.Get("X-Next-Cursor") != next || res.Headers.Get("X-Prev-Cursor") != "" {
		t.Fatalf("cursors: %v", res.Headers)
	}
	if got, want := res.Headers.Get("Link"), `</api/posts?order=title.asc&limit=2&count=true&after=WyJwdWJsaWMucG9zdHMiLDJd>; rel="next"`; got != want {
		t.Fatalf("Link = %q\nwant %q", got, want)
	}

	// Page 2: both directions; the after= param is replaced in place, an
	// offset is dropped from cursor links.
	op.RawQuery = "order=title.asc&limit=2&count=true&after=WyJwdWJsaWMucG9zdHMiLDJd&offset=0"
	raw = []byte(`{"rows":[{"id":13},{"id":21}],"has_next":true,"has_prev":true,"first":[13],"last":[21],"total":5}`)
	res, _ = Unwrap(op, stmt, raw, true, "/api")
	if got, want := res.Headers.Get("Link"),
		`</api/posts?order=title.asc&limit=2&count=true&after=WyJwdWJsaWMucG9zdHMiLDIxXQ>; rel="next", </api/posts?order=title.asc&limit=2&count=true&before=WyJwdWJsaWMucG9zdHMiLDEzXQ>; rel="prev"`; got != want {
		t.Fatalf("Link = %q\nwant %q", got, want)
	}
	if res.Headers.Get("X-Prev-Cursor") != "WyJwdWJsaWMucG9zdHMiLDEzXQ" || res.Headers.Get("X-Next-Cursor") != "WyJwdWJsaWMucG9zdHMiLDIxXQ" {
		t.Fatalf("cursors: %v", res.Headers)
	}
	// Empty page: no cursors, no Link, [] body.
	raw = []byte(`{"rows":[],"has_next":false,"has_prev":true,"first":null,"last":null,"total":5}`)
	res, _ = Unwrap(op, stmt, raw, true, "/api")
	if string(res.Data) != `[]` || res.Headers.Get("Link") != "" || res.Headers.Get("X-Prev-Cursor") != "" {
		t.Fatalf("empty page: %s %v", res.Data, res.Headers)
	}
}

func TestUnwrapPageOffset(t *testing.T) {
	b := built(t)
	op := listOp(b, "/metrics")
	op.Plan.Page = plan.Page{Limit: 20, Offset: 40}
	op.RawQuery = "limit=20&offset=40"
	stmt := &compile.Statement{Shape: compile.ShapePage}
	raw := []byte(`{"rows":[{"name":"x"}],"has_next":true,"has_prev":true}`)
	res, err := Unwrap(op, stmt, raw, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Headers.Get("X-Next-Cursor") != "" || res.Headers.Get("X-Prev-Cursor") != "" || res.Headers.Get("X-Total-Count") != "" {
		t.Fatalf("offset-only must not emit cursors: %v", res.Headers)
	}
	if got, want := res.Headers.Get("Link"), `</metrics?limit=20&offset=60>; rel="next", </metrics?limit=20&offset=20>; rel="prev"`; got != want {
		t.Fatalf("Link = %q\nwant %q", got, want)
	}
	op.Plan.Page = plan.Page{Limit: 20}
	op.RawQuery = ""
	raw = []byte(`{"rows":[{"name":"x"}],"has_next":true,"has_prev":false}`)
	res, _ = Unwrap(op, stmt, raw, true, "/api")
	if got := res.Headers.Get("Link"); got != `</api/metrics?offset=20>; rel="next"` {
		t.Fatalf("Link = %q", got)
	}
}

func TestWithParam(t *testing.T) {
	cases := []struct{ raw, key, val, want string }{
		{"", "after", "c", "?after=c"},
		{"a=1&after=x&b=2", "after", "c", "?a=1&after=c&b=2"},
		{"a=1&after=x&b=2&after=y", "after", "c", "?a=1&after=c&b=2"},
		{"a=1&before=x&offset=3", "after", "c", "?a=1&after=c"},
		{"select=id,email&limit=2", "offset", "4", "?select=id,email&limit=2&offset=4"},
		{"mood=eq.happy&offset=20&limit=20", "offset", "40", "?mood=eq.happy&offset=40&limit=20"},
	}
	for _, tc := range cases {
		if got := withParam(tc.raw, tc.key, tc.val, "before", "offset"); got != tc.want {
			t.Errorf("withParam(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// ---- database error mapping ----

func TestDBErrorMapping(t *testing.T) {
	b := built(t)
	cases := []struct {
		code   string
		status int
	}{
		{"22P02", 400}, {"23502", 400}, {"23514", 400}, {"42703", 400},
		{"42501", 403},
		{"23505", 409}, {"23503", 409}, {"23001", 409}, {"23P01", 409}, {"40001", 409}, {"40P01", 409},
		{"P0001", 500}, {"XX000", 500},
		{"53300", 503}, {"08006", 503}, {"57P01", 503},
		{"57014", 504},
	}
	for _, tc := range cases {
		e := newExec(&fakeDB{row: fakeRow{err: &pgconn.PgError{Code: tc.code, Message: "m", TableName: "users", ConstraintName: "c"}}}, b, nil, Options{})
		res := e.Execute(context.Background(), listOp(b, "/users"))
		xe := errOf(t, res)
		if xe.Status != tc.status || xe.Code != "pg:"+tc.code {
			t.Errorf("%s: got %d %s, want %d", tc.code, xe.Status, xe.Code, tc.status)
		}
		if xe.PG != nil {
			t.Errorf("%s: pg object leaked outside dev", tc.code)
		}
	}
	// dev: pg object; prod: message policy.
	pgErr := &pgconn.PgError{Code: "23505", Message: "dup", Detail: "Key (email)=(x) already exists.", TableName: "users", ConstraintName: "users_email_key"}
	e := newExec(&fakeDB{row: fakeRow{err: pgErr}}, b, nil, Options{Detail: pgexec.Dev})
	xe := errOf(t, e.Execute(context.Background(), listOp(b, "/users")))
	if xe.Detail != "dup" || xe.PG["sqlstate"] != "23505" || xe.PG["constraint"] != "users_email_key" || xe.PG["table"] != "users" || xe.PG["detail"] == nil {
		t.Fatalf("dev: %+v", xe)
	}
	e = newExec(&fakeDB{row: fakeRow{err: &pgconn.PgError{Code: "P0001", Message: "nope"}}}, b, nil, Options{Detail: pgexec.Prod})
	if xe = errOf(t, e.Execute(context.Background(), listOp(b, "/users"))); xe.Detail != "database error" {
		t.Fatalf("prod P0001: %+v", xe)
	}
	e = newExec(&fakeDB{row: fakeRow{err: &pgconn.PgError{Code: "23505", Message: "dup"}}}, b, nil, Options{Detail: pgexec.Strict})
	if xe = errOf(t, e.Execute(context.Background(), listOp(b, "/users"))); xe.Detail != "constraint violation" {
		t.Fatalf("strict 23505: %+v", xe)
	}
	for _, tc := range []struct {
		err                *pgconn.PgError
		prodDetail, strict string
	}{
		{&pgconn.PgError{Code: "XX000", Message: "ST_Intersects: Operation on mixed SRID geometries (Point, 3857) != (Point, 4326)"}, "invalid geometry", "invalid input value"},
		{&pgconn.PgError{Code: "XX000", Message: "parse error - invalid geometry"}, "invalid geometry", "invalid input value"},
		{&pgconn.PgError{Code: "42601", Message: `syntax error in tsquery: "a &&"`}, `syntax error in tsquery: "a &&"`, "invalid input value"},
		{&pgconn.PgError{Code: "42704", Message: `text search configuration "nope" does not exist`}, `text search configuration "nope" does not exist`, "invalid input value"},
	} {
		for detail, want := range map[pgexec.Detail]string{pgexec.Prod: tc.prodDetail, pgexec.Strict: tc.strict, pgexec.Dev: tc.err.Message} {
			e = newExec(&fakeDB{row: fakeRow{err: tc.err}}, b, nil, Options{Detail: detail})
			xe = errOf(t, e.Execute(context.Background(), listOp(b, "/users")))
			if xe.Status != 400 || xe.Code != "pg:"+tc.err.Code || xe.Detail != want {
				t.Errorf("%s (detail %d): got %d %s %q, want 400 %q", tc.err.Message, detail, xe.Status, xe.Code, xe.Detail, want)
			}
		}
	}
	// non-PG errors
	e = newExec(&fakeDB{row: fakeRow{err: context.DeadlineExceeded}}, b, nil, Options{})
	if xe = errOf(t, e.Execute(context.Background(), listOp(b, "/users"))); xe.Status != 504 || xe.Code != CodeTimeout {
		t.Fatalf("deadline: %+v", xe)
	}
	e = newExec(&fakeDB{row: fakeRow{err: errors.New("weird")}}, b, nil, Options{})
	if xe = errOf(t, e.Execute(context.Background(), listOp(b, "/users"))); xe.Status != 500 || xe.Code != CodeInternal || xe.Detail != "internal error" {
		t.Fatalf("other: %+v", xe)
	}
}

// ---- end-to-end through the fake with real compilation ----

func TestExecuteListThroughFake(t *testing.T) {
	b := built(t)
	db := &fakeDB{row: fakeRow{data: []byte(`{"rows":[{"id":1}],"has_next":true,"has_prev":false,"first":[1],"last":[1],"total":3}`)}}
	e := newExec(db, b, nil, Options{BasePath: "/api"})
	op := listOp(b, "/users")
	op.Plan.Count = true
	op.RawQuery = "count=true"
	res := e.Execute(context.Background(), op)
	if res.Err != nil || res.Status != 200 || string(res.Data) != `[{"id":1}]` {
		t.Fatalf("res=%+v err=%v", res, res.Err)
	}
	if res.Headers.Get("X-Total-Count") != "3" || res.Headers.Get("X-Next-Cursor") == "" || !strings.HasPrefix(res.Headers.Get("Link"), "</api/users?count=true&after=") {
		t.Fatalf("headers: %v", res.Headers)
	}
	if len(db.queries) != 1 || !strings.Contains(db.queries[0], `FROM "public"."users"`) {
		t.Fatalf("queries: %v", db.queries)
	}
	if OperationFromContext(context.Background()) != nil {
		t.Fatal("bare context has no operation")
	}
}

func TestExecuteMaxAffected(t *testing.T) {
	b := built(t)
	users := b.ByPath["users"]
	bulkOp := func(limit *int64, applied ...string) *Operation {
		return &Operation{Method: "DELETE", Path: "/users", Resource: users, Mutation: true, Plan: &plan.Request{
			Method: plan.DELETE, Resource: users, Page: plan.Page{Limit: 100},
			Filter: plan.Cond{Column: users.Table.Column("id"), Op: plan.OpGt, Value: int64(0)},
			Prefer: plan.Prefer{Return: "representation", Strict: limit != nil, MaxAffected: limit, Applied: applied},
		}}
	}
	two := int64(2)
	three := int64(3)
	data := []byte(`{"n":3,"rows":[{"id":1},{"id":2},{"id":3}]}`)

	db := &fakeDB{row: fakeRow{data: data}}
	res := newExec(db, b, nil, Options{TxMutations: false}).Execute(context.Background(), bulkOp(&two, "handling=strict", "max-affected=2"))
	if xe := errOf(t, res); xe.Status != 400 || xe.Code != CodeMaxAffectedExceeded || !strings.Contains(xe.Detail, "affects 3 rows") {
		t.Fatalf("exceeded: %+v", xe)
	}
	if len(db.txs) != 1 || db.txs[0].committed || !db.txs[0].rolled {
		t.Fatalf("exceeded must run in a rolled-back tx even with transactions.mutations off: %+v", db.txs)
	}

	db = &fakeDB{row: fakeRow{data: data}}
	res = newExec(db, b, nil, Options{TxMutations: true}).Execute(context.Background(), bulkOp(&three, "handling=strict", "max-affected=3"))
	if res.Err != nil || res.Status != 200 || len(db.txs) != 1 || !db.txs[0].committed {
		t.Fatalf("within cap: %+v %v", res, db.txs)
	}
	if got := res.Headers.Get("Preference-Applied"); got != "handling=strict, max-affected=3" {
		t.Errorf("Preference-Applied = %q", got)
	}

	db = &fakeDB{row: fakeRow{data: data}}
	res = newExec(db, b, nil, Options{TxMutations: false}).Execute(context.Background(), bulkOp(nil))
	if res.Err != nil || len(db.txs) != 0 || res.Headers.Get("Preference-Applied") != "" {
		t.Fatalf("no cap: %+v txs=%d", res, len(db.txs))
	}
}
