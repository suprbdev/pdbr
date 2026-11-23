package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbcore/pgexec"

	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/plan"
)

// partialBulk reports a bulk POST or PUT running under Prefer: partial=items.
func partialBulk(op *Operation) bool {
	return op != nil && op.Plan != nil && op.Plan.Prefer.Partial && op.Plan.Body != nil && len(op.Plan.Body.Objects) > 0
}

// partialItem is the outcome of one element of a partial bulk write.
type partialItem struct {
	status int
	row    json.RawMessage
	err    *Error
}

// runPartial executes a bulk POST or PUT one element at a time in a
// pgexec.RunTx transaction, each element in its own savepoint. A failing
// element is rolled back to its savepoint and reported; the others commit
// with the transaction. tzStmt, the Prefer: timezone= statement, runs once
// before the first element.
//
// A serialization failure or deadlock is not an element failure: it aborts
// the transaction, which RunTx re-runs up to transactions.max_retries times,
// and when the retries run out the request is a plain 409 and nothing was
// written. A rolled-back request (server.tx_end, Prefer: tx=rollback) runs
// every element and reports them, then discards them all.
func (e *Executor) runPartial(ctx context.Context, op *Operation, applied []string, tzStmt []pgexec.Stmt) *Result {
	body := op.Plan.Body
	items := make([]partialItem, len(body.Objects))
	runTx := e.runTx
	if runTx == nil {
		runTx = pgexec.RunTx
	}
	err := runTx(ctx, e.DB, pgexec.TxOptions{
		Isolation:  e.Opts.Isolation,
		MaxRetries: e.Opts.MaxRetries,
		RLS:        e.Opts.RLS,
		Log:        e.Opts.Logger,
		Observer:   e.Opts.Metrics.DBObserver(),
	}, pgexec.Identity{
		Role:         op.Role,
		Claims:       op.Claims,
		ClaimsPrefix: e.Opts.ClaimsPrefix,
	}, func(q pgexec.Querier) error {
		for _, s := range tzStmt {
			if _, err := q.Exec(ctx, s.SQL, s.Args...); err != nil {
				return err
			}
		}
		for i := range body.Objects {
			items[i] = partialItem{}
			it := &items[i]
			err := pgexec.Savepoint(ctx, q, func() error { return e.runItem(ctx, q, op, i, it) })
			var rolledBack *pgexec.RolledBackError
			switch {
			case errors.As(err, &rolledBack):
				it.status, it.row, it.err = 0, nil, e.itemError(ctx, op, rolledBack.Err)
			case err != nil:
				return err
			}
		}
		if rollsBack(op) {
			return pgexec.ErrRollback
		}
		return nil
	})
	if err != nil && !errors.Is(err, pgexec.ErrRollback) {
		var xe *Error
		if errors.As(err, &xe) {
			return e.errResult(xe)
		}
		return e.errResult(e.dbError(ctx, op, err))
	}
	res := e.partialResult(op, items)
	if len(applied) > 0 {
		res.Headers.Set("Preference-Applied", strings.Join(applied, ", "))
	}
	return res
}

// runItem compiles and runs element i of the bulk body as a statement of
// its own and records its row.
func (e *Executor) runItem(ctx context.Context, q pgexec.Querier, op *Operation, i int, it *partialItem) error {
	p := *op.Plan
	p.Body = &plan.Body{Objects: op.Plan.Body.Objects[i : i+1], Columns: op.Plan.Body.Columns}
	stmt, err := e.Compile(ctx, &compile.Request{
		Plan:          &p,
		Built:         e.Built,
		MaxEmbedDepth: e.Opts.MaxEmbedDepth,
		MaxPageSize:   e.Opts.MaxPageSize,
	})
	if err != nil {
		return e.compileError(err)
	}
	var raw []byte
	err = q.QueryRow(ctx, stmt.SQL, stmt.Args...).Scan(&raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	it.status = http.StatusCreated
	if op.Method == string(plan.PUT) {
		it.status = http.StatusOK
	}
	if op.Plan.Prefer.Minimal() {
		var w struct {
			N int64 `json:"n"`
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &w); err != nil {
				return &Error{Status: http.StatusInternalServerError, Code: CodeInternal, Detail: "malformed bulk mutation wrapper"}
			}
		}
		if w.N == 0 {
			it.status = http.StatusNoContent
		}
		return nil
	}
	var rows []json.RawMessage
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &rows); err != nil {
			return &Error{Status: http.StatusInternalServerError, Code: CodeInternal, Detail: "malformed bulk mutation wrapper"}
		}
	}
	if len(rows) == 0 {
		it.status = http.StatusNoContent
		return nil
	}
	it.row = rows[0]
	return nil
}

// itemError renders the failure of one element like a failed request.
func (e *Executor) itemError(ctx context.Context, op *Operation, err error) *Error {
	var xe *Error
	if errors.As(err, &xe) {
		return xe
	}
	err = e.dbError(ctx, op, err)
	if errors.As(err, &xe) {
		return xe
	}
	return &Error{Status: http.StatusInternalServerError, Code: CodeInternal, Detail: "internal error"}
}

// partialResult renders the per-element report: 201 (200 for PUT) when
// every element succeeded, 207 when at least one failed.
//
//	{"n": 2, "failed": 1, "items": [
//	  {"index": 0, "status": 201, "row": {...}},
//	  {"index": 1, "status": 409, "code": "pg:23505", "detail": "..."}]}
func (e *Executor) partialResult(op *Operation, items []partialItem) *Result {
	res := &Result{Status: http.StatusCreated, Headers: http.Header{}}
	if op.Method == string(plan.PUT) {
		res.Status = http.StatusOK
	}
	var ok, failed int
	var b bytes.Buffer
	for i, it := range items {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"index":` + strconv.Itoa(i))
		if it.err != nil {
			failed++
			b.WriteString(`,"status":` + strconv.Itoa(it.err.Status))
			writeField(&b, "code", it.err.Code)
			writeField(&b, "detail", it.err.Detail)
			if len(it.err.PG) > 0 {
				pg, _ := json.Marshal(it.err.PG)
				b.WriteString(`,"pg":`)
				b.Write(pg)
			}
			b.WriteByte('}')
			continue
		}
		ok++
		b.WriteString(`,"status":` + strconv.Itoa(it.status))
		if it.row != nil {
			b.WriteString(`,"row":`)
			b.Write(it.row)
		}
		b.WriteByte('}')
	}
	if failed > 0 {
		res.Status = http.StatusMultiStatus
	}
	res.Data = json.RawMessage(`{"n":` + strconv.Itoa(ok) + `,"failed":` + strconv.Itoa(failed) + `,"items":[` + b.String() + `]}`)
	return res
}

func writeField(b *bytes.Buffer, key, val string) {
	enc, _ := json.Marshal(val)
	b.WriteString(`,"` + key + `":`)
	b.Write(enc)
}
