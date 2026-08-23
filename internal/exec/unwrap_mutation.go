package exec

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/plan"
)

// CodeEtagMismatch is the code of a 412 on a stale If-Match.
const CodeEtagMismatch = "etag_mismatch"

type mutationWrapper struct {
	Row      json.RawMessage `json:"row"`
	Found    *bool           `json:"found"`
	Inserted *bool           `json:"inserted"`
	Key      []any           `json:"key"`
	N        *int64          `json:"n"`
}

// unwrapMutation maps the mutation shapes (compile.ShapeMutRow / MutList /
// MutWrap) to status, Location and body per docs/mutations.md: 201 for
// POST and for a single-object PUT that inserted, 200 otherwise, 204 under
// Prefer: return=minimal (201 + Location stays for creates); zero rows on a
// keyed path 404; If-Match found-but-unchanged 412 etag_mismatch.
func unwrapMutation(op *Operation, stmt *compile.Statement, raw []byte, found bool, basePath string) (*Result, error) {
	res := &Result{Status: http.StatusOK, Headers: http.Header{}}
	method := plan.Method(op.Method)
	minimal := op.Plan != nil && op.Plan.Prefer.Return == "minimal"
	switch stmt.Shape {
	case compile.ShapeMutRow:
		if !found || raw == nil {
			return nil, notFound(op)
		}
		res.Data = raw
		return res, nil
	case compile.ShapeMutList:
		if !found || raw == nil {
			raw = json.RawMessage("[]")
		}
		if method == plan.POST {
			res.Status = http.StatusCreated
		}
		res.Data = raw
		return res, nil
	}
	if !found || raw == nil {
		return nil, notFound(op)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var w mutationWrapper
	if err := dec.Decode(&w); err != nil {
		return nil, &Error{Status: http.StatusInternalServerError, Code: CodeInternal, Detail: "malformed mutation wrapper"}
	}
	if w.Found != nil && !*w.Found {
		return nil, notFound(op)
	}
	rowNull := len(w.Row) == 0 || bytes.Equal(w.Row, []byte("null"))
	switch {
	case w.N != nil && *w.N == 0:
		return nil, notFound(op)
	case w.N == nil && rowNull && w.Found != nil:
		return nil, &Error{Status: http.StatusPreconditionFailed, Code: CodeEtagMismatch, Detail: "row has changed since it was read"}
	case w.N == nil && rowNull:
		return nil, notFound(op)
	}
	created := method == plan.POST || (method == plan.PUT && w.Inserted != nil && *w.Inserted)
	switch {
	case created:
		res.Status = http.StatusCreated
	case minimal:
		res.Status = http.StatusNoContent
	}
	if created {
		if loc := locationFor(op, basePath, w.Key); loc != "" {
			res.Headers.Set("Location", loc)
		}
	}
	if !minimal {
		res.Data = w.Row
	}
	return res, nil
}

// locationFor renders {base}/{table}/{pk} for a single-column primary key.
func locationFor(op *Operation, basePath string, key []any) string {
	if op == nil || op.Resource == nil || len(key) != 1 || key[0] == nil {
		return ""
	}
	var v string
	switch x := key[0].(type) {
	case json.Number:
		v = x.String()
	case string:
		v = x
	case bool:
		if x {
			v = "true"
		} else {
			v = "false"
		}
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return ""
		}
		v = string(b)
	}
	return basePath + op.Resource.URLPath() + "/" + url.PathEscape(v)
}
