package exec

import "context"

type ctxKey struct{}

// WithOperation attaches the in-flight operation to ctx so CompileHooks can
// read claims/role/metadata via OperationFromContext.
func WithOperation(ctx context.Context, op *Operation) context.Context {
	return context.WithValue(ctx, ctxKey{}, op)
}

// OperationFromContext returns the in-flight operation, or nil.
func OperationFromContext(ctx context.Context) *Operation {
	op, _ := ctx.Value(ctxKey{}).(*Operation)
	return op
}

// ClaimsFromContext returns the verified claims of the in-flight operation.
func ClaimsFromContext(ctx context.Context) map[string]any {
	if op := OperationFromContext(ctx); op != nil {
		return op.Claims
	}
	return nil
}
