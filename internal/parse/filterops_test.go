package parse

import (
	"net/url"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"

	"github.com/suprbdev/pdbr/internal/plan"
)

// TestFilterOpsMatchParser pins the OpenAPI operator vocabulary
// (api.Built.FilterOps) to what the parser actually accepts: for every
// filterable column of the fixture and every operator, the request parses
// iff the operator is documented for the column.
func TestFilterOpsMatchParser(t *testing.T) {
	built := buildFixture(t, false)
	all := []plan.Op{plan.OpEq, plan.OpNeq, plan.OpGt, plan.OpGte, plan.OpLt, plan.OpLte, plan.OpIn, plan.OpIs,
		plan.OpLike, plan.OpILike, plan.OpSw, plan.OpEw, plan.OpCs, plan.OpCd, plan.OpOv, plan.OpHk, plan.OpJpe, plan.OpJpm}
	for _, path := range []string{"users", "posts", "places", "events"} {
		res := built.ByPath[path]
		for _, f := range res.Columns {
			if !res.FilterColumns[f.Key] {
				continue
			}
			documented := map[string]bool{}
			for _, op := range built.FilterOps(f.Column) {
				documented[op] = true
			}
			for _, op := range all {
				val := sampleValue(op, f.Column)
				u := "/api/" + path + "?" + f.Key + "=" + string(op) + "." + url.QueryEscape(val)
				_, err := doParse(t, built, req{url: u}, allOn)
				if (err == nil) != documented[string(op)] {
					t.Errorf("%s.%s %s: parse err=%v, documented=%v (url %s)", path, f.Key, op, err, documented[string(op)], u)
				}
			}
		}
	}
}

func sampleValue(op plan.Op, c *introspect.Column) string {
	base := strings.TrimPrefix(c.PGType, "_")
	isArray := c.IsArray || strings.HasPrefix(c.PGType, "_")
	scalar := func() string {
		switch {
		case c.EnumRef != "":
			return "conference"
		case base == "mood":
			return "happy"
		}
		switch base {
		case "int2", "int4", "int8":
			return "1"
		case "float4", "float8", "numeric":
			return "1.5"
		case "bool":
			return "true"
		case "uuid":
			return "9f2c1a2e-1111-4222-8333-444455556666"
		case "timestamptz", "timestamp":
			return "2020-01-01T00:00:00Z"
		case "jsonb", "json":
			return `{"a":1}`
		}
		return "x"
	}
	switch op {
	case plan.OpIs:
		return "null"
	case plan.OpIn:
		return "(" + scalar() + ")"
	case plan.OpLike, plan.OpILike, plan.OpSw, plan.OpEw:
		return "x"
	case plan.OpHk:
		return "a"
	case plan.OpJpe, plan.OpJpm:
		return "$.a"
	}
	if isArray {
		return "{" + scalar() + "}"
	}
	return scalar()
}
