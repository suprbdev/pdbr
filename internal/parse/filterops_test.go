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
// filterable column of the fixture plus the contrib-typed kb tables (the
// cube, earth, point and raster ones with the postgis plugin applied),
// under both filter policies, and every operator (core, contrib and the
// raster st_intersects), the request parses iff the operator is documented for
// the column.
func TestFilterOpsMatchParser(t *testing.T) {
	all := []string{"eq", "neq", "gt", "gte", "lt", "lte", "in", "is", "isdistinct",
		"like", "ilike", "sw", "ew", "match", "imatch", "cs", "cd", "ov", "hk", "jpe", "jpm"}
	for _, name := range api.ContribFilterNames() {
		if !slices.Contains(all, name) {
			all = append(all, name)
		}
	}
	for _, indexedOnly := range []bool{false, true} {
		for _, set := range []struct {
			built *api.Built
			paths []string
		}{
			{buildContrib(t, indexedOnly), []string{"users", "posts", "places", "events", "kb.docs", "kb.embeddings", "kb.labels"}},
			{buildGeo(t, indexedOnly), []string{"kb.shapes", "kb.sites", "kb.tiles"}},
		} {
			built := set.built
			for _, path := range set.paths {
				res := built.ByPath[path]
				for _, f := range append(append([]*api.Field(nil), res.Columns...), res.Rasters...) {
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
						switch api.ThresholdParam(op) {
						case api.ParamVec:
							u = "/api/" + strings.ReplaceAll(path, ".", "/") + "?" + f.Key + "=" + op + ".0.5&~vec=" + url.QueryEscape(sampleVector(f.Column))
						case api.ParamLevenshtein:
							u = "/api/" + strings.ReplaceAll(path, ".", "/") + "?" + f.Key + "=" + op + ".2&~levenshtein=x"
						case api.ParamLatLon:
							u = "/api/" + strings.ReplaceAll(path, ".", "/") + "?" + f.Key + "=" + op + ".2&~latlon=1,2"
						}
						_, err := doParse(t, built, req{url: u}, allOn)
						if (err == nil) != documented[op] {
							t.Errorf("indexed_only=%v %s.%s %s: parse err=%v, documented=%v (url %s)", indexedOnly, path, f.Key, op, err, documented[op], u)
						}
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
	mods := []string{"rank", "rank_cd", "trgm_distance", "trgm_word_distance", "trgm_strict_word_distance", "l2_distance", "negative_inner_product", "cosine_distance", "l1_distance", "chebyshev_distance",
		"hamming_distance", "jaccard_distance", "levenshtein_distance", "earth_distance", "earth_miles_distance"}
	for _, indexedOnly := range []bool{false, true} {
		for _, set := range []struct {
			built *api.Built
			paths []string
		}{
			{buildContrib(t, indexedOnly), []string{"kb.docs", "kb.embeddings"}},
			{buildGeo(t, indexedOnly), []string{"kb.shapes", "kb.sites"}},
		} {
			built := set.built
			for _, path := range set.paths {
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
						switch api.DistanceParam(mod) {
						case api.ParamVec:
							q += "&~vec=" + url.QueryEscape(sampleVector(f.Column))
						case api.ParamLevenshtein:
							q += "&~levenshtein=x"
						case api.ParamLatLon:
							q += "&~latlon=1,2"
						case api.ParamTrgm:
							q += "&~trgm=x"
						}
						if strings.HasPrefix(mod, "rank") && res.FilterColumns[f.Key] {
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
}

// sampleVector is a ~vec= reference for column c: a bit string for bit
// columns, a JSON array otherwise.
func sampleVector(c *introspect.Column) string {
	if c.PGType == "bit" || c.PGType == "varbit" {
		return "101"
	}
	return "[1,2,3]"
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
		case "cube":
			return "(1,2)"
		case "point":
			return "(1,2)"
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
	case "fts", "plfts", "phfts", "wfts", "trgm_sim", "trgm_wsim", "trgm_swsim", "ancestor", "descendant", "lquery", "ltxtquery",
		"hk_def", "soundex", "metaphone", "dmetaphone":
		return "a"
	case "query_int":
		return "1&2"
	case "intersects":
		return "POINT(1 2)"
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
