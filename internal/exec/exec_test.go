package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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
// SQL it saw. Transactions go through queryTx (installed by newExec in
// place of pgexec.QueryTx), which records each batch as a fakeTx.
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

func (f *fakeDB) Acquire(context.Context) (*pgxpool.Conn, error) {
	return nil, errors.New("fakeDB: transactions go through queryTx")
}

func (f *fakeDB) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	return nil, errors.New("fakeDB: partial writes go through runTx")
}

// fakeTx is one pgexec.QueryTx batch as the fake saw it.
type fakeTx struct {
	opts      pgexec.TxOptions
	id        pgexec.Identity
	stmts     []string
	committed bool
	rolled    bool
}

// queryTx mimics pgexec.QueryTx: the role is validated before anything is
// sent, every statement's Scan gets the configured row, a statement error
// (the row's err) rolls back, and an error of the Scan callback itself
// rolls back only under ScanBeforeCommit (otherwise the batch has already
// committed).
func (f *fakeDB) queryTx(_ context.Context, _ pgexec.ConnAcquirer, opts pgexec.TxOptions, id pgexec.Identity, stmts ...pgexec.Stmt) error {
	if opts.RLS && id.Role != "" && !pgexec.ValidRole(id.Role) {
		return fmt.Errorf("invalid role %q", id.Role)
	}
	tx := &fakeTx{opts: opts, id: id}
	f.txs = append(f.txs, tx)
	for _, st := range stmts {
		tx.stmts = append(tx.stmts, st.SQL)
		f.queries = append(f.queries, st.SQL)
		if st.Scan == nil {
			continue
		}
		if err := st.Scan(f.row); err != nil {
			stmtErr := f.row.err != nil && errors.Is(err, f.row.err)
			tx.rolled = stmtErr || opts.ScanBeforeCommit
			tx.committed = !tx.rolled
			return err
		}
	}
	tx.committed = true
	return nil
}

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
	e := New(db, b, compile.New(b).Compile, hooks, opts)
	if f, ok := db.(*fakeDB); ok {
		e.queryTx = f.queryTx
	}
	return e
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
				if tx.opts.RLS != tc.opts.RLS || tx.id.Role != tc.role || len(tx.id.Claims) != len(tc.claims) {
					t.Fatalf("identity = %+v rls %v", tx.id, tx.opts.RLS)
				}
				if tx.opts.Isolation != "" || tx.opts.ScanBeforeCommit {
					t.Fatalf("read committed without max-affected must be one implicit batch: %+v", tx.opts)
				}
				if len(tx.stmts) != 1 || len(db.queries) != 1 {
					t.Fatalf("statements = %v, queries = %v", tx.stmts, db.queries)
				}
			}
		})
	}
}

func TestTransactionBatchOptions(t *testing.T) {
	b := built(t)
	page := []byte(`{"rows":[],"has_next":false,"has_prev":false}`)
	for _, tc := range []struct {
		iso  pgx.TxIsoLevel
		want pgx.TxIsoLevel
	}{
		{"", ""},
		{pgx.ReadCommitted, ""},
		{pgx.RepeatableRead, pgx.RepeatableRead},
		{pgx.Serializable, pgx.Serializable},
	} {
		db := &fakeDB{row: fakeRow{data: page}}
		e := newExec(db, b, nil, Options{RLS: true, Isolation: tc.iso, MaxRetries: 2, ClaimsPrefix: "app.claims"})
		op := listOp(b, "/users")
		op.Role, op.Claims = "app_user", map[string]any{"user_id": "1"}
		if res := e.Execute(context.Background(), op); res.Err != nil {
			t.Fatalf("%q: %v", tc.iso, res.Err)
		}
		got := db.txs[0]
		if got.opts.Isolation != tc.want || got.opts.MaxRetries != 2 || got.opts.ScanBeforeCommit || got.id.ClaimsPrefix != "app.claims" {
			t.Errorf("isolation %q: batch %+v id %+v", tc.iso, got.opts, got.id)
		}
	}
}

func TestTransactionErrors(t *testing.T) {
	b := built(t)
	db := &fakeDB{}
	e := newExec(db, b, nil, Options{RLS: true})
	op := listOp(b, "/users")
	op.Role = "none"
	if xe := errOf(t, e.Execute(context.Background(), op)); xe.Status != 500 || len(db.queries) != 0 {
		t.Fatalf("role none: %+v, queries %v", xe, db.queries)
	}

	db = &fakeDB{row: fakeRow{err: &pgconn.PgError{Code: "42501", Message: "permission denied for table users"}}}
	e = newExec(db, b, nil, Options{RLS: true})
	op = listOp(b, "/users")
	op.Role = "anonymous"
	if xe := errOf(t, e.Execute(context.Background(), op)); xe.Status != 403 || xe.Code != "pg:42501" {
		t.Fatalf("statement error: %+v", xe)
	}
	if len(db.txs) != 1 || db.txs[0].committed || !db.txs[0].rolled {
		t.Fatalf("statement error must roll back: %+v", db.txs[0])
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
	raw := []byte(`{"has_next":true,"has_prev":false,"first":[7],"last":[2],"total":5,"n":2,"rows":[{"id":7},{"id":2}]}`)
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
	raw = []byte(`{"has_next" : true, "has_prev" : true, "first" : [13], "last" : [21], "total" : 5, "n" : 2, "rows" : [{"id" : 13}, {"id" : 21}]}`)
	res, _ = Unwrap(op, stmt, raw, true, "/api")
	if got, want := res.Headers.Get("Link"),
		`</api/posts?order=title.asc&limit=2&count=true&after=WyJwdWJsaWMucG9zdHMiLDIxXQ>; rel="next", </api/posts?order=title.asc&limit=2&count=true&before=WyJwdWJsaWMucG9zdHMiLDEzXQ>; rel="prev"`; got != want {
		t.Fatalf("Link = %q\nwant %q", got, want)
	}
	if res.Headers.Get("X-Prev-Cursor") != "WyJwdWJsaWMucG9zdHMiLDEzXQ" || res.Headers.Get("X-Next-Cursor") != "WyJwdWJsaWMucG9zdHMiLDIxXQ" {
		t.Fatalf("cursors: %v", res.Headers)
	}
	// Empty page: no cursors, no Link, [] body.
	raw = []byte(`{"has_next":false,"has_prev":true,"first":null,"last":null,"total":5,"n":0,"rows":[]}`)
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
	raw := []byte(`{"has_next":true,"has_prev":true,"n":1,"rows":[{"name":"x"}]}`)
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

func TestUnwrapPageFeatureCollection(t *testing.T) {
	b := built(t)
	op := listOp(b, "/metrics")
	op.Plan.Page = plan.Page{Limit: 20}
	stmt := &compile.Statement{Shape: compile.ShapePage}
	fc := `{"type":"FeatureCollection","features":[{"type":"Feature","geometry":null,"properties":{}}]}`
	raw := []byte(`{"has_next":false,"has_prev":false,"n":1,"rows":` + fc + `}`)
	res, err := Unwrap(op, stmt, raw, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Data) != fc || res.Headers.Get("Content-Range") != "0-0/*" {
		t.Fatalf("data=%s headers=%v", res.Data, res.Headers)
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
	guardSQL := compile.GuardSQL("__c_1", `nested "posts.update[0]":`)
	start := strings.Index(guardSQL, "'pdbr guard ") + 1
	marker := guardSQL[start : strings.Index(guardSQL[start:], ": ")+start+2]
	for _, detail := range []pgexec.Detail{pgexec.Dev, pgexec.Prod, pgexec.Strict} {
		guard := &pgconn.PgError{Code: "22P02", Message: `invalid input syntax for type boolean: "` + marker + `nested "posts.update[0]": matched 0 rows"`}
		e := newExec(&fakeDB{row: fakeRow{err: guard}}, b, nil, Options{Detail: detail})
		xe := errOf(t, e.Execute(context.Background(), listOp(b, "/users")))
		if xe.Status != 404 || xe.Code != CodeNotFound || xe.Detail != `nested "posts.update[0]": matched 0 rows` || xe.PG != nil {
			t.Errorf("guard failure under %v: %+v", detail, xe)
		}
		forged := &pgconn.PgError{Code: "22P02", Message: `invalid input value for enum mood: "pdbr guard: everything is fine, nothing matched"`}
		e = newExec(&fakeDB{row: fakeRow{err: forged}}, b, nil, Options{Detail: detail})
		if xe := errOf(t, e.Execute(context.Background(), listOp(b, "/users"))); xe.Status != 400 || xe.Code != "pg:22P02" {
			t.Errorf("forged guard under %v: %+v", detail, xe)
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
	if res.Err != nil || res.Status != 206 || string(res.Data) != `[{"id":1}]` {
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
	if len(db.txs) != 1 || db.txs[0].committed || !db.txs[0].rolled || !db.txs[0].opts.ScanBeforeCommit {
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

func TestContentRange(t *testing.T) {
	b := built(t)
	stmt := &compile.Statement{Shape: compile.ShapePage, Total: true}
	cases := []struct {
		page plan.Page
		raw  string
		want string
	}{
		{plan.Page{Limit: 2}, `{"rows":[{"id":1},{"id":2}],"has_next":true,"has_prev":false,"total":5}`, "0-1/5"},
		{plan.Page{Limit: 2, Offset: 4}, `{"rows":[{"id":5}],"has_next":false,"has_prev":true,"total":5}`, "4-4/5"},
		{plan.Page{Limit: 2, Offset: 5}, `{"rows":[],"has_next":false,"has_prev":true,"total":5}`, "*/5"},
		{plan.Page{Limit: 2, After: []any{1}}, `{"rows":[{"id":2}],"has_next":false,"has_prev":true,"total":5}`, ""},
	}
	for _, tc := range cases {
		op := listOp(b, "/users")
		op.Plan.Page = tc.page
		res, err := Unwrap(op, stmt, []byte(tc.raw), true, "")
		if err != nil {
			t.Fatal(err)
		}
		if got := res.Headers.Get("Content-Range"); got != tc.want {
			t.Errorf("%+v: Content-Range = %q, want %q", tc.page, got, tc.want)
		}
	}
	op := listOp(b, "/users")
	res, _ := Unwrap(op, &compile.Statement{Shape: compile.ShapePage}, []byte(`{"rows":[{"id":1}],"has_next":false,"has_prev":false}`), true, "")
	if got := res.Headers.Get("Content-Range"); got != "0-0/*" || res.Headers.Get("X-Total-Count") != "" {
		t.Errorf("no count: %q", got)
	}
	res, _ = Unwrap(op, &compile.Statement{Shape: compile.ShapePage}, nil, false, "")
	if res.Headers.Get("Content-Range") != "" {
		t.Errorf("no row: %v", res.Headers)
	}

	users := b.ByPath["users"]
	bulk := func(prefer string, count bool) *Operation {
		return &Operation{Method: "PATCH", Resource: users, Plan: &plan.Request{Method: plan.PATCH, Count: count, Prefer: plan.Prefer{Return: prefer}}}
	}
	mb := &compile.Statement{Shape: compile.ShapeMutBulk}
	for _, tc := range []struct {
		op   *Operation
		raw  string
		want string
	}{
		{bulk("", false), `{"n":2,"rows":[{},{}]}`, "0-1/*"},
		{bulk("", true), `{"n":2,"rows":[{},{}]}`, "0-1/2"},
		{bulk("", true), `{"n":0,"rows":[]}`, "*/0"},
		{bulk("minimal", true), `{"n":3}`, "*/3"},
		{bulk("minimal", false), `{"n":3}`, "*/*"},
	} {
		res, err := Unwrap(tc.op, mb, []byte(tc.raw), true, "")
		if err != nil || res.Headers.Get("Content-Range") != tc.want {
			t.Errorf("%s: Content-Range = %q %v, want %q", tc.raw, res.Headers.Get("Content-Range"), err, tc.want)
		}
	}
}

func TestPageStatus(t *testing.T) {
	b := built(t)
	stmt := &compile.Statement{Shape: compile.ShapePage, Total: true}
	cases := []struct {
		name   string
		page   plan.Page
		raw    string
		status int
		cr     string
	}{
		{"whole result", plan.Page{Limit: 100}, `{"rows":[{"id":1},{"id":2}],"total":2}`, 200, "0-1/2"},
		{"strict subset", plan.Page{Limit: 1}, `{"rows":[{"id":1}],"total":2}`, 206, "0-0/2"},
		{"last partial page", plan.Page{Limit: 2, Offset: 4}, `{"rows":[{"id":5}],"total":5}`, 206, "4-4/5"},
		{"offset equals total", plan.Page{Limit: 2, Offset: 5}, `{"rows":[],"total":5}`, 206, "*/5"},
		{"empty table", plan.Page{Limit: 2}, `{"rows":[],"total":0}`, 200, "*/0"},
		{"no total", plan.Page{Limit: 1}, `{"rows":[{"id":1}]}`, 200, "0-0/*"},
	}
	for _, tc := range cases {
		st := stmt
		if tc.name == "no total" {
			st = &compile.Statement{Shape: compile.ShapePage}
		}
		op := listOp(b, "/users")
		op.Plan.Page = tc.page
		res, err := Unwrap(op, st, []byte(tc.raw), true, "")
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if res.Status != tc.status || res.Headers.Get("Content-Range") != tc.cr {
			t.Errorf("%s: status=%d Content-Range=%q, want %d %q", tc.name, res.Status, res.Headers.Get("Content-Range"), tc.status, tc.cr)
		}
	}

	op := listOp(b, "/users")
	op.Plan.Page = plan.Page{Limit: 2, Offset: 9}
	_, err := Unwrap(op, stmt, []byte(`{"rows":[],"total":5}`), true, "")
	var xe *Error
	if !errors.As(err, &xe) || xe.Status != 416 || xe.Code != CodeRangeNotSatisfiable || xe.Headers.Get("Content-Range") != "*/5" {
		t.Fatalf("offset past total: %+v", err)
	}
}

func TestRangeLinkCarriesLimit(t *testing.T) {
	b := built(t)
	op := listOp(b, "/users")
	op.RawQuery = "order=id"
	op.Plan.Page = plan.Page{Limit: 10, Offset: 10, LimitSet: true, FromRange: true}
	res, err := Unwrap(op, &compile.Statement{Shape: compile.ShapePage}, []byte(`{"rows":[{"id":1}],"has_next":true,"has_prev":true}`), true, "/api")
	if err != nil {
		t.Fatal(err)
	}
	link := res.Headers.Get("Link")
	for _, want := range []string{"</api/users?order=id&offset=20&limit=10>; rel=\"next\"", "</api/users?order=id&offset=0&limit=10>; rel=\"prev\""} {
		if !strings.Contains(link, want) {
			t.Errorf("Link = %q, want %q", link, want)
		}
	}
}

func TestExecuteRollback(t *testing.T) {
	b := built(t)
	users := b.ByPath["users"]
	data := []byte(`{"n":1,"rows":[{"id":1}]}`)
	op := func(pf plan.Prefer) *Operation {
		pf.Return = "representation"
		return &Operation{Method: "DELETE", Path: "/users", Resource: users, Mutation: true, Plan: &plan.Request{
			Method: plan.DELETE, Resource: users, Page: plan.Page{Limit: 100},
			Filter: plan.Cond{Column: users.Table.Column("id"), Op: plan.OpGt, Value: int64(0)},
			Prefer: pf,
		}}
	}
	db := &fakeDB{row: fakeRow{data: data}}
	res := newExec(db, b, nil, Options{TxMutations: false}).Execute(context.Background(), op(plan.Prefer{Rollback: true, Applied: []string{"tx=rollback"}}))
	if res.Err != nil || res.Status != 200 || string(res.Data) != `[{"id":1}]` {
		t.Fatalf("rollback result: %+v", res)
	}
	if len(db.txs) != 1 || db.txs[0].committed || !db.txs[0].rolled || !db.txs[0].opts.ScanBeforeCommit {
		t.Fatalf("rollback must discard the tx: %+v", db.txs)
	}
	if got := res.Headers.Get("Preference-Applied"); got != "tx=rollback" {
		t.Errorf("Preference-Applied = %q", got)
	}

	db = &fakeDB{row: fakeRow{err: errors.New("boom")}}
	res = newExec(db, b, nil, Options{}).Execute(context.Background(), op(plan.Prefer{Rollback: true}))
	if res.Err == nil {
		t.Fatalf("a failing statement must still be an error: %+v", res)
	}
}

func TestExecuteTimezone(t *testing.T) {
	b := built(t)
	e := newExec(&fakeDB{row: fakeRow{data: []byte(`{"rows":[],"has_next":false,"has_prev":false,"n":0}`)}}, b, nil, Options{})
	e.tzNames = map[string]string{"asia/tokyo": "Asia/Tokyo"}
	db := e.DB.(*fakeDB)
	op := listOp(b, "/users")
	op.Plan.Prefer = plan.Prefer{Return: "representation", Timezone: "asia/TOKYO"}
	res := e.Execute(context.Background(), op)
	if res.Err != nil || len(db.txs) != 1 || len(db.txs[0].stmts) != 2 || db.txs[0].stmts[0] != timezoneSQL {
		t.Fatalf("timezone batch: %+v %+v", res, db.txs)
	}
	if got := res.Headers.Get("Preference-Applied"); got != "timezone=Asia/Tokyo" {
		t.Errorf("Preference-Applied = %q", got)
	}

	db.txs = nil
	op = listOp(b, "/users")
	op.Plan.Prefer = plan.Prefer{Return: "representation", Timezone: "Mars/Base"}
	res = e.Execute(context.Background(), op)
	if res.Err != nil || len(db.txs) != 0 || res.Headers.Get("Preference-Applied") != "" {
		t.Fatalf("unknown zone must be ignored: %+v txs=%d", res, len(db.txs))
	}

	op.Plan.Prefer.Strict = true
	res = e.Execute(context.Background(), op)
	if xe := errOf(t, res); xe.Status != 400 || xe.Code != CodeInvalidPreference {
		t.Fatalf("strict unknown zone: %+v", xe)
	}
}
