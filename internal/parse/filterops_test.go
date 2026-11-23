package parse

import (
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"

	"github.com/suprbdev/pdbr/internal/api"
)

// TestFilterOpsMatchParser pins the OpenAPI operator vocabulary
// (api.Built.FilterOps) to what the parser actually accepts: for every
// filterable column of the fixture plus the contrib-typed kb tables, under
// both filter policies, and every operator (core and contrib), the request
// parses iff the operator is documented for the column.
func TestFilterOpsMatchParser(t *testing.T) {
	all := []string{"eq", "neq", "gt", "gte", "lt", "lte", "in", "is", "isdistinct",
		"like", "ilike", "sw", "ew", "match", "imatch", "cs", "cd", "ov", "hk", "jpe", "jpm"}
	for _, name := range api.ContribFilterNames() {
		if !slices.Contains(all, name) {
			all = append(all, name)
		}
	}
	for _, indexedOnly := range []bool{false, true} {
		built := buildContrib(t, indexedOnly)
		for _, path := range []string{"users", "posts", "places", "events", "kb.docs", "kb.embeddings"} {
			res := built.ByPath[path]
			for _, f := range res.Columns {
				if !res.FilterColumns[f.Key] {
					continue
				}
				documented := map[string]bool{}
				for _, op := range built.FilterOps(res, f.Column) {
					documented[op] = true
				}
				for _, op := range all {
					val := sampleValue(op, f.Column)
					u := "/api/" + strings.ReplaceAll(path, ".", "/") + "?" + f.Key + "=" + op + "." + url.QueryEscape(val)
					_, err := doParse(t, built, req{url: u}, allOn)
					if (err == nil) != documented[op] {
						t.Errorf("indexed_only=%v %s.%s %s: parse err=%v, documented=%v (url %s)", indexedOnly, path, f.Key, op, err, documented[op], u)
					}
				}
			}
		}
	}
}

// TestContribOrdersMatchParser pins the documented contrib order terms
// (api.Built.ContribOrders, rendered into the order parameter) to the
// parser.
func TestContribOrdersMatchParser(t *testing.T) {
	mods := []string{"rank", "rank_cd", "trgm_dist", "trgm_wdist", "trgm_swdist", "vec_l2", "vec_ip", "vec_cos", "vec_l1"}
	for _, indexedOnly := range []bool{false, true} {
		built := buildContrib(t, indexedOnly)
		for _, path := range []string{"kb.docs", "kb.embeddings"} {
			res := built.ByPath[path]
			for _, f := range res.Columns {
				documented := map[string]bool{}
				if res.OrderColumns[f.Key] {
					for _, o := range built.ContribOrders(res, f.Column) {
						documented[o.Name] = o.Rank == "" || res.FilterColumns[f.Key]
					}
				}
				for _, mod := range mods {
					q := "order=" + f.Key + "." + mod + ".desc"
					switch {
					case strings.HasPrefix(mod, "vec_"):
						q += "&vec=" + url.QueryEscape("[1,2,3]")
					case !strings.HasPrefix(mod, "rank"):
						q += "&trgm=x"
					case res.FilterColumns[f.Key]:
						q += "&" + f.Key + "=fts.x"
					}
					_, err := doParse(t, built, req{url: "/api/" + strings.ReplaceAll(path, ".", "/") + "?" + q}, allOn)
					if (err == nil) != documented[mod] {
						t.Errorf("indexed_only=%v %s %s.%s: parse err=%v, documented=%v", indexedOnly, path, f.Key, mod, err, documented[mod])
					}
				}
			}
		}
	}
}

func sampleValue(op string, c *introspect.Column) string {
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
		case "int4range":
			return "[1,5)"
		case "daterange":
			return "[2020-01-01,2020-02-01)"
		case "int4multirange":
			return "{[1,5)}"
		case "hstore":
			return "a=>1"
		case "inet":
			return "10.0.0.0/8"
		case "ltree":
			return "a.b"
		}
		return "x"
	}
	switch op {
	case "is":
		return "null"
	case "in":
		return `("` + scalar() + `")`
	case "like", "ilike", "sw", "ew", "match", "imatch":
		return "x"
	case "hk":
		return "a"
	case "jpe", "jpm":
		return "$.a"
	case "hk_all", "hk_any", "ancestor_any", "descendant_any", "lquery_any":
		return "{a,b}"
	case "fts", "plfts", "phfts", "wfts", "trgm_sim", "trgm_wsim", "trgm_swsim", "ancestor", "descendant", "lquery", "ltxtquery":
		return "a"
	case "sl", "sr", "nxr", "nxl", "adj", "ov", "cs", "cd":
		switch base {
		case "int4multirange":
			return "[1,5)"
		case "hstore":
			return `{"a":"1"}`
		}
	}
	if isArray {
		return "{" + scalar() + "}"
	}
	return scalar()
}
