package exec

import (
	"encoding/json"
	"net/http"

	"github.com/suprbdev/pdbcore/jwtmint"

	"github.com/suprbdev/pdbr/internal/compile"
)

// unwrapRPC handles the RPC-only shapes: void → 204 without a body; scalar,
// composite, single-row and set-returning values pass through verbatim (a
// missing row renders as null); the jwt shapes pass the raw claims through
// for Executor.run to sign. Reports false for shapes it does not own.
func unwrapRPC(res *Result, stmt *compile.Statement, raw []byte, found bool) bool {
	switch stmt.Shape {
	case compile.ShapeVoid:
		res.Status = http.StatusNoContent
		res.Data = nil
	case compile.ShapeScalar, compile.ShapeJWT, compile.ShapeJWTSet:
		if !found || raw == nil {
			res.Data = json.RawMessage("null")
		} else {
			res.Data = raw
		}
	default:
		return false
	}
	return true
}

// mint signs the claims of a jwt-shaped result in place.
func (e *Executor) mint(res *Result, stmt *compile.Statement) error {
	set := stmt.Shape == compile.ShapeJWTSet
	if stmt.Shape != compile.ShapeJWT && !set {
		return nil
	}
	if !e.Opts.Mint.Enabled() {
		return &Error{Status: http.StatusInternalServerError, Code: CodeInternal, Detail: "jwt minting is not configured (rls.auth.jwt_type / jwt_secret)"}
	}
	minted, err := jwtmint.Mint(e.Opts.Mint, res.Data, set)
	if err != nil {
		e.Opts.Logger.Error("jwt mint failed", "err", err)
		return &Error{Status: http.StatusInternalServerError, Code: CodeInternal, Detail: "could not mint token"}
	}
	res.Data = minted
	return nil
}
