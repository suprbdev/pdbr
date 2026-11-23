// Package exec is the thin glue between the compiled statement and
// pdbcore/pgexec: it builds the Operation RequestHooks see, applies the
// read-only gate and the cost limit, compiles through the CompileHook
// chain, decides the transaction policy, runs the statement, and unwraps
// the statement's data column into status, headers and body.
package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/suprbdev/pdbcore/jwtmint"
	"github.com/suprbdev/pdbcore/metrics"
	"github.com/suprbdev/pdbcore/pgexec"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/plan"
)

// Operation is one in-flight HTTP request as RequestHooks see it.
type Operation struct {
	Method string
	// Path is the request path below base_path, e.g. /posts/7.
	Path string
	// RawQuery is the request query string (Link headers rewrite it).
	RawQuery string
	Plan     *plan.Request
	Resource *api.Resource // the table/view addressed, nil for RPC
	RPC      *api.RPC      // the function addressed, nil for resources
	Claims   map[string]any
	Role     string
	Mutation bool
	ForceTx  bool
}

// Result is what execution produced.
type Result struct {
	Status  int
	Headers http.Header     // ETag, X-Total-Count, Content-Range, X-Next-Cursor, X-Prev-Cursor, Link, Location, Allow, Preference-Applied
	Data    json.RawMessage // the client JSON (already unwrapped); nil for 204/304/HEAD or on error
	Err     error           // *Error; the server renders problem+json
}

// Error is a failed request: everything the server needs for the problem
// body. Status is repeated in Result.Status.
type Error struct {
	Status int
	Code   string
	Detail string
	// PG is the errors.detail=dev extension object (nil otherwise).
	PG map[string]any
	// Allow is set on 405 responses.
	Allow string
	// Headers are extra response headers (Content-Range on 416).
	Headers http.Header
}

func (e *Error) Error() string { return e.Detail }

// Error codes rendered by the executor (docs/errors.md).
const (
	CodeNotFound    = "not_found"
	CodeReadOnly    = "read_only"
	CodeInternal    = "internal"
	CodeTimeout     = "timeout"
	CodeUnavailable = "unavailable"
	// CodeMaxAffectedExceeded: a PATCH / DELETE under Prefer: handling=strict,
	// max-affected=N touched more than N rows; the transaction is rolled back.
	CodeMaxAffectedExceeded = "max_affected_exceeded"
	// CodeInvalidPreference: Prefer: timezone= names a zone pg_timezone_names
	// does not list, under handling=strict.
	CodeInvalidPreference = "invalid_preference"
	// CodeRangeNotSatisfiable: the requested offset lies beyond the counted
	// total (416 with Content-Range: */<total>).
	CodeRangeNotSatisfiable = "range_not_satisfiable"
)

// RequestHook mirrors plugin.RequestHook (redeclared here to avoid an
// import cycle; the interfaces are structurally identical).
type RequestHook interface {
	BeforeRequest(ctx context.Context, op *Operation) (context.Context, error)
	AfterRequest(ctx context.Context, op *Operation, res *Result)
}

// DB is the pool surface the executor needs (satisfied by *pgxpool.Pool):
// QueryRow for statements outside a transaction, Acquire for the pipelined
// transactions of pgexec.QueryTx, BeginTx for the statement-by-statement
// transactions of partial bulk writes.
type DB interface {
	pgexec.Querier
	pgexec.ConnAcquirer
	pgexec.TxBeginner
}

// Options are the config bits the executor consults per request.
type Options struct {
	Isolation  pgx.TxIsoLevel
	MaxRetries int
	// ClaimsPrefix and ClaimsJSONSetting are claims.prefix and
	// claims.json_setting: where the request claims are set for SQL.
	ClaimsPrefix      string
	ClaimsJSONSetting string
	// Detail is the errors.detail policy.
	Detail pgexec.Detail
	// MaxCost / MaxEmbedDepth / MaxPageSize are limits.max_cost, max_depth and max_page_size.
	MaxCost       int
	MaxEmbedDepth int
	MaxPageSize   int
	// BasePath prefixes Link header URLs.
	BasePath string
	// ReadOnly, when non-nil and true, answers every mutation with 405
	// before it compiles or touches the database. Shared with the App so a
	// runtime flip survives watch-mode swaps.
	ReadOnly *atomic.Bool
	// Mint signs the results of auth.mint_type-returning functions.
	Mint   jwtmint.Options
	Logger *slog.Logger
	// Metrics records database operation durations and errors and the
	// compile time of every request; nil records nothing.
	Metrics *metrics.Metrics
	// CostRejections is incremented for every request MaxCost rejects;
	// nil records nothing.
	CostRejections *metrics.Counter
	// AggregateRequests is incremented for every executed request whose
	// select= uses an aggregate function anywhere in its tree; nil records
	// nothing.
	AggregateRequests *metrics.Counter
}

// Executor runs operations against a pool.
type Executor struct {
	DB      DB
	Built   *api.Built
	Compile compile.Func
	Hooks   []RequestHook
	Opts    Options

	tzMu    sync.Mutex
	tzNames map[string]string

	queryTx func(ctx context.Context, db pgexec.ConnAcquirer, opts pgexec.TxOptions, id pgexec.Identity, stmts ...pgexec.Stmt) error
	// runTx replaces pgexec.RunTx for partial bulk writes in tests.
	runTx func(ctx context.Context, db pgexec.TxBeginner, opts pgexec.TxOptions, id pgexec.Identity, fn func(q pgexec.Querier) error) error
}

// New creates an executor. compileFn is normally the CompileHook chain
// around compile.New(built).Compile; db may be nil when only Prepare is
// used (pdbr request --print-sql).
func New(db DB, built *api.Built, compileFn compile.Func, hooks []RequestHook, opts Options) *Executor {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Isolation == "" {
		opts.Isolation = pgx.ReadCommitted
	}
	if opts.MaxCost <= 0 {
		opts.MaxCost = 10000
	}
	if opts.MaxPageSize <= 0 {
		opts.MaxPageSize = 100
	}
	if opts.MaxEmbedDepth <= 0 {
		opts.MaxEmbedDepth = 5
	}
	return &Executor{DB: db, Built: built, Compile: compileFn, Hooks: hooks, Opts: opts}
}

// Execute runs one operation end to end and never returns nil.
func (e *Executor) Execute(ctx context.Context, op *Operation) *Result {
	if op.Plan != nil && op.Plan.Select.Aggregates() {
		e.Opts.AggregateRequests.Inc()
	}
	ctx, stmt, err := e.prepare(ctx, op)
	var res *Result
	if err != nil {
		res = e.errResult(err)
	} else {
		res = e.run(ctx, op, stmt)
	}
	for _, h := range e.Hooks {
		h.AfterRequest(ctx, op, res)
	}
	return res
}

// Prepare runs the pipeline up to (not including) execution: RequestHooks,
// the read-only gate, the cost check and compilation. The database is never
// touched (pdbr request --print-sql).
func (e *Executor) Prepare(ctx context.Context, op *Operation) (*compile.Statement, error) {
	_, stmt, err := e.prepare(ctx, op)
	return stmt, err
}

func (e *Executor) prepare(ctx context.Context, op *Operation) (context.Context, *compile.Statement, error) {
	ctx = WithOperation(ctx, op)
	for _, h := range e.Hooks {
		ctx2, err := h.BeforeRequest(ctx, op)
		if err != nil {
			return ctx, nil, e.hookError(err)
		}
		ctx = ctx2
	}
	if op.Mutation && e.Opts.ReadOnly != nil && e.Opts.ReadOnly.Load() {
		return ctx, nil, &Error{Status: http.StatusMethodNotAllowed, Code: CodeReadOnly,
			Detail: "server is in read-only mode: writes are disabled"}
	}
	if cost := compile.Cost(op.Plan, e.Opts.MaxPageSize); cost > e.Opts.MaxCost {
		e.Opts.CostRejections.Inc()
		return ctx, nil, &Error{Status: http.StatusBadRequest, Code: compile.CodeCostExceeded,
			Detail: fmt.Sprintf("request cost %d exceeds limits.max_cost %d", cost, e.Opts.MaxCost)}
	}
	start := time.Now()
	stmt, err := e.Compile(ctx, &compile.Request{
		Plan:          op.Plan,
		Built:         e.Built,
		MaxEmbedDepth: e.Opts.MaxEmbedDepth,
		MaxPageSize:   e.Opts.MaxPageSize,
	})
	e.Opts.Metrics.ObserveCompile(time.Since(start))
	if err != nil {
		return ctx, nil, e.compileError(err)
	}
	return ctx, stmt, nil
}

// hookError renders a BeforeRequest failure: UserErrors are client-facing,
// anything else is a 500.
func (e *Executor) hookError(err error) error {
	var ue *compile.UserError
	if errors.As(err, &ue) {
		return fromUserError(ue)
	}
	e.Opts.Logger.Error("request hook failed", "err", err)
	return e.internal(err, "internal error")
}

// compileError renders a compile failure. Non-UserError compile errors are
// bugs (a plan the compiler cannot handle) and never leak their message
// outside dev mode: it can carry table/column/type names.
func (e *Executor) compileError(err error) error {
	var ue *compile.UserError
	if errors.As(err, &ue) {
		return fromUserError(ue)
	}
	e.Opts.Logger.Error("compile failed", "err", err)
	return e.internal(err, "compile: invalid request")
}

func (e *Executor) internal(err error, prodDetail string) error {
	detail := prodDetail
	if e.Opts.Detail == pgexec.Dev {
		detail = err.Error()
	}
	return &Error{Status: http.StatusInternalServerError, Code: CodeInternal, Detail: detail}
}

func fromUserError(ue *compile.UserError) *Error {
	status := ue.Status
	if status == 0 {
		status = http.StatusBadRequest
	}
	code := ue.Code
	if code == "" {
		code = compile.CodeInvalidParameter
	}
	return &Error{Status: status, Code: code, Detail: ue.Detail}
}

func (e *Executor) errResult(err error) *Result {
	var xe *Error
	if !errors.As(err, &xe) {
		xe = &Error{Status: http.StatusInternalServerError, Code: CodeInternal, Detail: "internal error"}
	}
	return &Result{Status: xe.Status, Headers: http.Header{}, Err: xe}
}

// needsTx is the transaction decision table (docs/architecture.md).
func (e *Executor) needsTx(op *Operation, stmt *compile.Statement, tz bool) bool {
	return op.ForceTx ||
		op.Mutation || stmt.Mutation ||
		maxAffected(op) != nil ||
		rollsBack(op) ||
		tz ||
		op.Role != "" ||
		(e.Opts.ClaimsPrefix != "" && len(op.Claims) > 0) ||
		e.Opts.ClaimsJSONSetting != ""
}

// identity is the role and claims op runs with.
func (e *Executor) identity(op *Operation) pgexec.Identity {
	return pgexec.Identity{
		Role:              op.Role,
		Claims:            op.Claims,
		ClaimsPrefix:      e.Opts.ClaimsPrefix,
		ClaimsJSONSetting: e.Opts.ClaimsJSONSetting,
	}
}

// run executes the statement (in a transaction when the policy asks for
// one) and unwraps its data column. A transaction is one pipelined batch
// (pgexec.QueryTx): identity and statement, plus BEGIN/COMMIT only for a
// non-default isolation level or a Prefer: max-affected cap, whose check
// must run before the commit.
func (e *Executor) run(ctx context.Context, op *Operation, stmt *compile.Statement) *Result {
	if e.DB == nil {
		return e.errResult(&Error{Status: http.StatusServiceUnavailable, Code: CodeUnavailable, Detail: "no database connection"})
	}
	var raw []byte
	var found bool
	scanRow := func(row pgx.Row) error {
		raw, found = nil, false
		err := row.Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		if limit := maxAffected(op); limit != nil {
			n, err := affectedRows(stmt, raw, found)
			if err != nil {
				return &Error{Status: http.StatusInternalServerError, Code: CodeInternal, Detail: "malformed mutation wrapper"}
			}
			if n > *limit {
				return &Error{Status: http.StatusBadRequest, Code: CodeMaxAffectedExceeded,
					Detail: fmt.Sprintf("the request affects %d rows, more than Prefer: max-affected=%d; nothing was written", n, *limit)}
			}
		}
		return nil
	}
	scan := func(row pgx.Row) error {
		err := scanRow(row)
		if err == nil && rollsBack(op) {
			return pgexec.ErrRollback
		}
		return err
	}
	applied, tzStmt, err := e.timezone(ctx, op)
	if err != nil {
		return e.errResult(err)
	}
	if partialBulk(op) {
		return e.runPartial(ctx, op, applied, tzStmt)
	}
	if e.needsTx(op, stmt, len(tzStmt) > 0) {
		queryTx := e.queryTx
		if queryTx == nil {
			queryTx = pgexec.QueryTx
		}
		err = queryTx(ctx, e.DB, pgexec.TxOptions{
			Isolation:        batchIsolation(e.Opts.Isolation),
			MaxRetries:       e.Opts.MaxRetries,
			RLS:              true,
			ScanBeforeCommit: maxAffected(op) != nil || rollsBack(op),
			Log:              e.Opts.Logger,
			Observer:         e.Opts.Metrics.DBObserver(),
		}, e.identity(op), append(tzStmt, pgexec.Stmt{SQL: stmt.SQL, Args: stmt.Args, Scan: scan})...)
		if errors.Is(err, pgexec.ErrRollback) {
			err = nil
		}
	} else {
		start := time.Now()
		err = scan(e.DB.QueryRow(ctx, stmt.SQL, stmt.Args...))
		dbErr := err
		var xe *Error
		if errors.As(err, &xe) {
			dbErr = nil
		}
		pgexec.Observe(ctx, e.Opts.Metrics.DBObserver(), "query", start, dbErr)
	}
	if err != nil {
		var xe *Error
		if errors.As(err, &xe) {
			return e.errResult(xe)
		}
		return e.errResult(e.dbError(ctx, op, err))
	}
	res, err := Unwrap(op, stmt, raw, found, e.Opts.BasePath)
	if err != nil {
		return e.errResult(err)
	}
	if len(applied) > 0 {
		res.Headers.Set("Preference-Applied", strings.Join(applied, ", "))
	}
	if err := e.mint(res, stmt); err != nil {
		return e.errResult(err)
	}
	return res
}

// rollsBack reports that the operation runs in a transaction that is rolled
// back (transactions.end or Prefer: tx=rollback).
func rollsBack(op *Operation) bool {
	return op != nil && op.Plan != nil && op.Plan.Prefer.Rollback
}

// timezoneSQL sets the transaction-local timezone from a bind parameter, so
// the statement text is constant.
const timezoneSQL = `SELECT set_config('timezone', $1, true)`

// timezone resolves Prefer: timezone= against pg_timezone_names. It returns
// the Preference-Applied tokens of the request (the parser's plus
// timezone=<canonical name>) and the statement that applies the zone before
// the request's own. A zone the database does not know is ignored, or
// 400 invalid_preference under handling=strict.
func (e *Executor) timezone(ctx context.Context, op *Operation) ([]string, []pgexec.Stmt, error) {
	if op == nil || op.Plan == nil {
		return nil, nil, nil
	}
	applied := op.Plan.Prefer.Applied
	want := op.Plan.Prefer.Timezone
	if want == "" {
		return applied, nil, nil
	}
	names, err := e.timezoneNames(ctx)
	if err != nil {
		return nil, nil, e.dbError(ctx, op, err)
	}
	name, ok := names[strings.ToLower(want)]
	if !ok {
		if op.Plan.Prefer.Strict {
			return nil, nil, &Error{Status: http.StatusBadRequest, Code: CodeInvalidPreference,
				Detail: fmt.Sprintf("invalid preference %q: unknown timezone (Prefer: handling=strict rejects unknown or malformed preferences)", "timezone="+want)}
		}
		return applied, nil, nil
	}
	applied = append(append([]string(nil), applied...), "timezone="+name)
	return applied, []pgexec.Stmt{{SQL: timezoneSQL, Args: []any{name}}}, nil
}

// timezoneNames returns pg_timezone_names keyed by lower-cased name, loaded
// on first use and kept for the life of the executor.
func (e *Executor) timezoneNames(ctx context.Context) (map[string]string, error) {
	e.tzMu.Lock()
	defer e.tzMu.Unlock()
	if e.tzNames != nil {
		return e.tzNames, nil
	}
	rows, err := e.DB.Query(ctx, `SELECT name FROM pg_timezone_names`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := map[string]string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names[strings.ToLower(n)] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	e.tzNames = names
	return names, nil
}

// batchIsolation is the pgexec.QueryTx isolation for transactions.isolation:
// read committed (the default) runs in the pipeline's implicit transaction
// at the server's default_transaction_isolation, saving the BEGIN/COMMIT;
// stricter levels need an explicit BEGIN ISOLATION LEVEL.
func batchIsolation(iso pgx.TxIsoLevel) pgx.TxIsoLevel {
	if iso == pgx.ReadCommitted {
		return ""
	}
	return iso
}

// maxAffected is the Prefer: max-affected cap of a resource write, or nil.
func maxAffected(op *Operation) *int64 {
	if op == nil || op.Plan == nil || op.Resource == nil {
		return nil
	}
	return op.Plan.Prefer.MaxAffected
}

func connectionLost(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET)
}

// dbError maps a database error to the status table in docs/errors.md.
func (e *Executor) dbError(ctx context.Context, op *Operation, err error) error {
	if ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &Error{Status: http.StatusGatewayTimeout, Code: CodeTimeout, Detail: "request timed out"}
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "22P02" {
		if detail, ok := compile.GuardFailure(pgErr.Message); ok {
			return &Error{Status: http.StatusNotFound, Code: CodeNotFound, Detail: detail}
		}
	}
	c := pgexec.Classify(err, e.Opts.Detail)
	if e.Opts.Detail == pgexec.Prod && c.PG != nil && strings.HasPrefix(c.SQLState, "23") && integrityHidden(e.Built, c.PG) {
		c.Message = integrityMessage(c.SQLState)
	}
	if c.SQLState == "" && (c.Class == pgexec.Resource || (c.Class == pgexec.Internal && connectionLost(err))) {
		// The connection was refused, reset or dropped without a SQLSTATE
		// (pool acquire, dial, mid-query EOF): the database was not
		// available, which is retryable — 503, not 500.
		c.Class = pgexec.Resource
		if e.Opts.Detail != pgexec.Dev {
			c.Message = "database unavailable"
		}
	}
	xe := &Error{Detail: c.Message}
	switch c.Class {
	case pgexec.UserInput:
		xe.Status = http.StatusBadRequest
	case pgexec.Permission:
		xe.Status = http.StatusForbidden
	case pgexec.Conflict:
		xe.Status = http.StatusConflict
	case pgexec.Resource:
		xe.Status = http.StatusServiceUnavailable
	case pgexec.Timeout:
		xe.Status = http.StatusGatewayTimeout
	default:
		xe.Status = http.StatusInternalServerError
	}
	switch {
	case c.SQLState != "":
		xe.Code = "pg:" + c.SQLState
	case c.Class == pgexec.Timeout:
		xe.Code = CodeTimeout
	case c.Class == pgexec.Resource:
		xe.Code = CodeUnavailable
	default:
		xe.Code = CodeInternal
	}
	if e.Opts.Detail == pgexec.Dev && c.PG != nil {
		pg := map[string]any{"sqlstate": c.PG.Code, "message": c.PG.Message}
		if c.PG.Detail != "" {
			pg["detail"] = c.PG.Detail
		}
		if c.PG.Hint != "" {
			pg["hint"] = c.PG.Hint
		}
		if c.PG.TableName != "" {
			pg["table"] = c.PG.TableName
		}
		if c.PG.ConstraintName != "" {
			pg["constraint"] = c.PG.ConstraintName
		}
		xe.PG = pg
	}
	if xe.Status >= 500 {
		e.Opts.Logger.Error("database error", "method", op.Method, "path", op.Path, "sqlstate", c.SQLState, "err", err)
	}
	return xe
}
