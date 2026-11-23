package compile

import (
	"fmt"
	"strings"

	"github.com/suprbdev/pdbcore/introspect"

	"github.com/suprbdev/pdbr/internal/plan"
)

// jsonPathSQL renders col's value at path over alias: json and jsonb
// columns directly, arrays and composites through to_jsonb (PostgREST's
// form, so an expression index on to_jsonb(col)->'k' applies). native keeps
// a json column json (selected values); everywhere else a json column is
// cast to jsonb, which has the comparison operators. Every key and index is
// a bind parameter: the SQL text depends only on the number of steps, their
// operators and their key/index kinds.
func (s *stmt) jsonPathSQL(alias string, col *introspect.Column, path plan.JSONPath, native bool) string {
	ref := colRef(alias, col.Name)
	base := strings.TrimPrefix(col.PGType, "_")
	switch {
	case col.IsArray || strings.HasPrefix(col.PGType, "_"):
		ref = "to_jsonb(" + ref + ")"
	case base == "jsonb":
	case base == "json":
		if !native {
			ref += "::jsonb"
		}
	default:
		ref = "to_jsonb(" + ref + ")"
	}
	var b strings.Builder
	b.WriteString("(" + ref)
	for _, st := range path {
		if st.Text {
			b.WriteString(" ->> ")
		} else {
			b.WriteString(" -> ")
		}
		if st.Array {
			b.WriteString(s.param(st.Index) + "::int4")
		} else {
			b.WriteString(s.param(st.Key) + "::text")
		}
	}
	b.WriteString(")")
	return b.String()
}

// pathColumn describes a JSON path's result as a column: text for ->>,
// jsonb for ->.
func pathColumn(col *introspect.Column, path plan.JSONPath) *introspect.Column {
	t := "jsonb"
	if path.Text() {
		t = "text"
	}
	return &introspect.Column{Name: col.Name, PGType: t, TypeSchema: "pg_catalog"}
}

// valueSQL renders the SQL value of a selected column item with a JSON
// path or a cast (not its JSON rendering): the path value, cast when a cast
// is given. native is jsonPathSQL's.
func (s *stmt) valueSQL(alias string, f *plan.FieldSel, native bool) string {
	expr := colRef(alias, f.Column.Name)
	if len(f.Path) > 0 {
		expr = s.jsonPathSQL(alias, f.Column, f.Path, native && f.Cast == nil)
	}
	if f.Cast != nil {
		expr = "(" + expr + ")::" + typeRefSQL(f.Cast)
	}
	return expr
}

// fieldValueJSON renders a selected column item with a JSON path or a cast for
// the response: a json path value or ->> text as it is, a cast value
// through to_json (numbers stay JSON numbers).
func (s *stmt) fieldValueJSON(alias string, f *plan.FieldSel) string {
	expr := s.valueSQL(alias, f, true)
	if f.Cast != nil {
		return "to_json(" + expr + ")"
	}
	return expr
}

// pairKey renders the response key of a selected field in a
// json_build_object call: a literal for a catalog-derived key, a bind
// parameter when the request supplies the key (an alias, or the last key of
// a JSON path).
func (s *stmt) pairKey(f *plan.FieldSel, key string) string {
	if f != nil && (f.Alias != "" || f.PathKeyed()) {
		return s.keyParam(key)
	}
	return quoteLiteral(key)
}

// keyParam renders a request-supplied response key as a text bind
// parameter, keeping request text out of the SQL.
func (s *stmt) keyParam(key string) string {
	return s.param(key) + "::text"
}

// pathSignature identifies a column's JSON path, values included, for
// matching an order term to a grouping key.
func pathSignature(col *introspect.Column, path plan.JSONPath) string {
	return fmt.Sprintf("%s%#v", col.Name, path)
}
