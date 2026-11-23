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
	"sync/atomic"
	"syscall"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbcore/jwtmint"
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
	Headers http.Header     // ETag, X-Total-Count, X-Next-Cursor, X-Prev-Cursor, Link, Location, Allow, Preference-Applied
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
)

// RequestHook mirrors plugin.RequestHook (redeclared here to avoid an
// import cycle; the interfaces are structurally identical).
type RequestHook interface {
	BeforeRequest(ctx context.Context, op *Operation) (context.Context, error)
	AfterRequest(ctx context.Context, op *Operation, res *Result)
}

// DB is the pool surface the executor needs (satisfied by *pgxpool.Pool).
type DB interface {
	pgexec.Querier
	pgexec.TxBeginner
}

// Options are the config bits the executor consults per request.
type Options struct {
	// TxMutations wraps every mutation in a transaction (transactions.mutations).
	TxMutations bool
	Isolation   pgx.TxIsoLevel
	MaxRetries  int
	// RLS enables SET LOCAL ROLE + claims set_config (rls.enabled).
	RLS          bool
	ClaimsPrefix string
	// Detail is the errors.detail policy.
	Detail pgexec.Detail
	// MaxCost / MaxEmbedDepth / MaxPageSize are the server.* limits.
	MaxCost       int
	MaxEmbedDepth int
	MaxPageSize   int
	// BasePath prefixes Link header URLs.
	BasePath string
	// ReadOnly, when non-nil and true, answers every mutation with 405
	// before it compiles or touches the database. Shared with the App so a
	// runtime flip survives watch-mode swaps.
	ReadOnly *atomic.Bool
	// Mint signs the results of rls.auth.jwt_type-returning functions.
	Mint   jwtmint.Options
	Logger *slog.Logger
}

// Executor runs operations against a pool.
type Executor struct {
	DB      DB
	Built   *api.Built
	Compile compile.Func
	Hooks   []RequestHook
	Opts    Options
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
		return ctx, nil, &Error{Status: http.StatusBadRequest, Code: compile.CodeCostExceeded,
			Detail: fmt.Sprintf("request cost %d exceeds server.max_cost %d", cost, e.Opts.MaxCost)}
	}
	stmt, err := e.Compile(ctx, &compile.Request{
		Plan:          op.Plan,
		Built:         e.Built,
		MaxEmbedDepth: e.Opts.MaxEmbedDepth,
		MaxPageSize:   e.Opts.MaxPageSize,
	})
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
	if errors.Is(err, compile.ErrNotImplemented) {
		return &Error{Status: http.StatusNotImplemented, Code: compile.CodeNotImplemented, Detail: err.Error()}
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
		code = compile.CodeInvalidFilter
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
func (e *Executor) needsTx(op *Operation, stmt *compile.Statement) bool {
	return op.ForceTx ||
		((op.Mutation || stmt.Mutation) && e.Opts.TxMutations) ||
		maxAffected(op) != nil ||
		(e.Opts.RLS && (op.Role != "" || len(op.Claims) > 0))
}

// run executes the statement (in a transaction when the policy asks for
// one) and unwraps its data column.
func (e *Executor) run(ctx context.Context, op *Operation, stmt *compile.Statement) *Result {
	if e.DB == nil {
		return e.errResult(&Error{Status: http.StatusServiceUnavailable, Code: CodeUnavailable, Detail: "no database connection"})
	}
	var raw []byte
	var found bool
	query := func(q pgexec.Querier) error {
		raw, found = nil, false
		err := q.QueryRow(ctx, stmt.SQL, stmt.Args...).Scan(&raw)
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
	var err error
	if e.needsTx(op, stmt) {
		err = pgexec.RunTx(ctx, e.DB, pgexec.TxOptions{
			Isolation:  e.Opts.Isolation,
			MaxRetries: e.Opts.MaxRetries,
			RLS:        e.Opts.RLS,
			Log:        e.Opts.Logger,
		}, pgexec.Identity{
			Role:         op.Role,
			Claims:       op.Claims,
			ClaimsPrefix: e.Opts.ClaimsPrefix,
		}, query)
	} else {
		err = query(e.DB)
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
	if op.Plan != nil && len(op.Plan.Prefer.Applied) > 0 {
		res.Headers.Set("Preference-Applied", strings.Join(op.Plan.Prefer.Applied, ", "))
	}
	if err := e.mint(res, stmt); err != nil {
		return e.errResult(err)
	}
	return res
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
	c := pgexec.Classify(err, e.Opts.Detail)
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
