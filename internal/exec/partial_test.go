package exec

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/pgexec"

	"github.com/suprbdev/pdbr/internal/plan"
)

// partialTx records every statement of the transaction and answers each
// element's INSERT by the email bound as its first argument.
type partialTx struct {
	log    []string
	answer map[string]fakeRow
}

func (f *partialTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("not used")
}

func (f *partialTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	f.log = append(f.log, sql)
	return pgconn.CommandTag{}, nil
}

func (f *partialTx) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	f.log = append(f.log, "INSERT")
	for _, a := range args {
		if s, ok := a.(string); ok {
			if r, ok := f.answer[s]; ok {
				return r
			}
		}
	}
	return fakeRow{data: []byte(`[{"id":1}]`)}
}

type partialRun struct {
	tx    *partialTx
	opts  pgexec.TxOptions
	calls int
	// after is an error RunTx returns once fn succeeded (a failed COMMIT).
	after error
}

func (r *partialRun) runTx(_ context.Context, _ pgexec.TxBeginner, opts pgexec.TxOptions, _ pgexec.Identity, fn func(pgexec.Querier) error) error {
	r.opts = opts
	r.calls++
	if err := fn(r.tx); err != nil {
		return err
	}
	return r.after
}

func bulkInsertOp(t *testing.T, method string, mutate func(*plan.Prefer), emails ...string) *Operation {
	t.Helper()
	b := built(t)
	users := b.ByPath["users"]
	objs := make([]map[string]any, len(emails))
	for i, em := range emails {
		objs[i] = map[string]any{"email": em}
	}
	pf := plan.Prefer{Return: "representation", Partial: true, Applied: []string{"partial=items"}}
	if mutate != nil {
		mutate(&pf)
	}
	return &Operation{Method: method, Path: "/users", Resource: users, Mutation: true, Plan: &plan.Request{
		Method: plan.Method(method), Resource: users,
		Body:   &plan.Body{Objects: objs, Columns: []*introspect.Column{users.Table.Column("email")}},
		Prefer: pf,
	}}
}

func TestPartialBulkReportsEachElement(t *testing.T) {
	b := built(t)
	dup := &pgconn.PgError{Code: "23505", Message: "duplicate key value violates unique constraint", ConstraintName: "users_email_key"}
	tx := &partialTx{answer: map[string]fakeRow{
		"a@x.io": {data: []byte(`[{"id":1,"email":"a@x.io"}]`)},
		"b@x.io": {err: dup},
		"c@x.io": {data: []byte(`[{"id":3,"email":"c@x.io"}]`)},
	}}
	run := &partialRun{tx: tx}
	e := newExec(&fakeDB{}, b, nil, Options{MaxRetries: 2, Detail: pgexec.Prod})
	e.runTx = run.runTx
	res := e.Execute(context.Background(), bulkInsertOp(t, "POST", nil, "a@x.io", "b@x.io", "c@x.io"))
	if res.Err != nil || res.Status != http.StatusMultiStatus {
		t.Fatalf("status = %d err = %v, want 207", res.Status, res.Err)
	}
	var got struct {
		N      int `json:"n"`
		Failed int `json:"failed"`
		Items  []struct {
			Index  int             `json:"index"`
			Status int             `json:"status"`
			Row    json.RawMessage `json:"row"`
			Code   string          `json:"code"`
			Detail string          `json:"detail"`
		} `json:"items"`
	}
	if err := json.Unmarshal(res.Data, &got); err != nil {
		t.Fatalf("%v: %s", err, res.Data)
	}
	if got.N != 2 || got.Failed != 1 || len(got.Items) != 3 {
		t.Fatalf("report = %s", res.Data)
	}
	if it := got.Items[0]; it.Status != 201 || string(it.Row) != `{"id":1,"email":"a@x.io"}` {
		t.Errorf("item 0 = %+v", it)
	}
	if it := got.Items[1]; it.Status != 409 || it.Code != "pg:23505" || it.Row != nil || it.Detail == "" {
		t.Errorf("item 1 = %+v", it)
	}
	if it := got.Items[2]; it.Status != 201 || it.Index != 2 {
		t.Errorf("item 2 = %+v", it)
	}
	if h := res.Headers.Get("Preference-Applied"); h != "partial=items" {
		t.Errorf("Preference-Applied = %q", h)
	}
	if run.opts.MaxRetries != 2 {
		t.Errorf("RunTx MaxRetries = %d, want the configured 2", run.opts.MaxRetries)
	}
	want := []string{
		"SAVEPOINT pdb_savepoint", "INSERT", "RELEASE SAVEPOINT pdb_savepoint",
		"SAVEPOINT pdb_savepoint", "INSERT", "ROLLBACK TO SAVEPOINT pdb_savepoint", "RELEASE SAVEPOINT pdb_savepoint",
		"SAVEPOINT pdb_savepoint", "INSERT", "RELEASE SAVEPOINT pdb_savepoint",
	}
	if !reflect.DeepEqual(tx.log, want) {
		t.Fatalf("statements = %v, want %v", tx.log, want)
	}
}

func TestPartialBulkAllSucceed(t *testing.T) {
	b := built(t)
	for method, want := range map[string]int{"POST": 201, "PUT": 200} {
		run := &partialRun{tx: &partialTx{}}
		e := newExec(&fakeDB{}, b, nil, Options{})
		e.runTx = run.runTx
		op := bulkInsertOp(t, method, nil, "a@x.io", "b@x.io")
		if method == "PUT" {
			op.Plan.OnConflict = op.Resource.Table.PrimaryKey
		}
		res := e.Execute(context.Background(), op)
		if res.Err != nil || res.Status != want || !strings.HasPrefix(string(res.Data), `{"n":2,"failed":0,"items":[`) {
			t.Fatalf("%s: status = %d err = %v data = %s", method, res.Status, res.Err, res.Data)
		}
	}
}

func TestPartialBulkMinimalAndSkippedElements(t *testing.T) {
	tx := &partialTx{answer: map[string]fakeRow{
		"a@x.io": {data: []byte(`{"n":1}`)},
		"b@x.io": {data: []byte(`{"n":0}`)},
	}}
	run := &partialRun{tx: tx}
	e := newExec(&fakeDB{}, built(t), nil, Options{})
	e.runTx = run.runTx
	res := e.Execute(context.Background(), bulkInsertOp(t, "POST", func(p *plan.Prefer) { p.Return = "minimal" }, "a@x.io", "b@x.io"))
	if want := `{"n":2,"failed":0,"items":[{"index":0,"status":201},{"index":1,"status":204}]}`; string(res.Data) != want {
		t.Fatalf("body = %s, want %s", res.Data, want)
	}
}

func TestPartialBulkRollbackRunsEverythingAndDiscardsIt(t *testing.T) {
	var got error
	e := newExec(&fakeDB{}, built(t), nil, Options{})
	e.runTx = func(_ context.Context, _ pgexec.TxBeginner, _ pgexec.TxOptions, _ pgexec.Identity, fn func(pgexec.Querier) error) error {
		got = fn(&partialTx{})
		return got
	}
	res := e.Execute(context.Background(), bulkInsertOp(t, "POST", func(p *plan.Prefer) { p.Rollback = true }, "a@x.io", "b@x.io"))
	if !errors.Is(got, pgexec.ErrRollback) {
		t.Fatalf("transaction callback returned %v, want ErrRollback so nothing commits", got)
	}
	if res.Err != nil || res.Status != 201 || !strings.Contains(string(res.Data), `"n":2`) {
		t.Fatalf("status = %d err = %v data = %s", res.Status, res.Err, res.Data)
	}
}

func TestPartialBulkTransactionFailures(t *testing.T) {
	serial := &pgconn.PgError{Code: "40001", Message: "could not serialize access"}
	t.Run("serialization failure in an element is not an element error", func(t *testing.T) {
		tx := &partialTx{answer: map[string]fakeRow{"b@x.io": {err: serial}}}
		run := &partialRun{tx: tx}
		e := newExec(&fakeDB{}, built(t), nil, Options{})
		e.runTx = run.runTx
		res := e.Execute(context.Background(), bulkInsertOp(t, "POST", nil, "a@x.io", "b@x.io", "c@x.io"))
		if res.Err == nil || res.Status != http.StatusConflict || res.Data != nil {
			t.Fatalf("status = %d err = %v data = %s, want a plain 409", res.Status, res.Err, res.Data)
		}
		for _, s := range tx.log {
			if strings.HasPrefix(s, "ROLLBACK TO") {
				t.Fatalf("serialization failure was rolled back to the savepoint: %v", tx.log)
			}
		}
	})
	t.Run("commit failure drops the report", func(t *testing.T) {
		run := &partialRun{tx: &partialTx{}, after: serial}
		e := newExec(&fakeDB{}, built(t), nil, Options{})
		e.runTx = run.runTx
		res := e.Execute(context.Background(), bulkInsertOp(t, "POST", nil, "a@x.io"))
		if res.Err == nil || res.Status != http.StatusConflict {
			t.Fatalf("status = %d err = %v", res.Status, res.Err)
		}
	})
}

func TestPartialBulkOffByDefault(t *testing.T) {
	db := &fakeDB{row: fakeRow{data: []byte(`[{"id":1}]`)}}
	e := newExec(db, built(t), nil, Options{})
	e.runTx = func(context.Context, pgexec.TxBeginner, pgexec.TxOptions, pgexec.Identity, func(pgexec.Querier) error) error {
		t.Fatal("RunTx used without Prefer: partial=items")
		return nil
	}
	op := bulkInsertOp(t, "POST", func(p *plan.Prefer) { p.Partial = false; p.Applied = nil }, "a@x.io", "b@x.io")
	res := e.Execute(context.Background(), op)
	if res.Err != nil || res.Status != 201 || string(res.Data) != `[{"id":1}]` {
		t.Fatalf("status = %d err = %v data = %s", res.Status, res.Err, res.Data)
	}
}
