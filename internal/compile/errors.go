package compile

import (
	"fmt"
	"net/http"
)

// Error codes emitted by the compiler (docs/errors.md).
const (
	CodeInvalidFilter      = "invalid_filter"
	CodeInvalidParameter   = "invalid_parameter"
	CodeInvalidArgument    = "invalid_argument"
	CodeInvalidSelect      = "invalid_select"
	CodePageSizeExceeded   = "page_size_exceeded"
	CodeKeysetUnsupported  = "keyset_unsupported"
	CodeEmbedDepthExceeded = "embed_depth_exceeded"
	CodeCostExceeded       = "cost_exceeded"
	CodeInvalidCursor      = "invalid_cursor"
	CodeInvalidBody        = "invalid_body"
	CodeMissingArgument    = "missing_argument"
	CodeNotAcceptable      = "not_acceptable"
	CodeColumnNotOrderable = "column_not_orderable"
	// CodeTemporalConflict rejects a temporal (WITHOUT OVERLAPS) key as an
	// upsert conflict target: ON CONFLICT cannot infer an exclusion
	// constraint (PostgreSQL would fail with 42P10).
	CodeTemporalConflict = "temporal_conflict_target"
)

// UserError is a client-facing error: rendered as a problem with the given
// status and code (parse errors and hook errors wrap it). Keep schema
// internals out of Detail; echoing back user-typed names is fine.
type UserError struct {
	Code   string
	Status int
	Detail string
}

func (e *UserError) Error() string { return e.Detail }

// NewUserError builds a 400 UserError.
func NewUserError(code, detail string) *UserError {
	return &UserError{Code: code, Status: http.StatusBadRequest, Detail: detail}
}

// errNotAcceptable is a 406 not_acceptable UserError.
func errNotAcceptable(detail string) error {
	return &UserError{Code: CodeNotAcceptable, Status: http.StatusNotAcceptable, Detail: detail}
}

func errUser(code, format string, args ...any) error {
	return NewUserError(code, fmt.Sprintf(format, args...))
}

// errPageSize rejects a limit above the page-size cap. Silently clamping
// would return fewer rows than asked for, which breaks paginators that infer
// "last page" from a short result.
func errPageSize(got, maxSize int) error {
	return errUser(CodePageSizeExceeded, "limit %d exceeds the maximum page size of %d", got, maxSize)
}
