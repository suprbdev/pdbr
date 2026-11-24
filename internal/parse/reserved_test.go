package parse

import (
	"reflect"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/plan"
	"github.com/suprbdev/pdbr/internal/testcatalog"
)

// TestReservedNamespace: the reference parameters and pseudo-columns live
// under api.ReservedPrefix, so columns, computed columns and function
// arguments named like them (near, trgm, vec, latlon, levenshtein, geometry,
// body_levenshtein_distance) change nothing: the bare names stay filters or
// arguments, the ~ names keep their meaning, and the surface needing them
// stays available.
func TestReservedNamespace(t *testing.T) {
	cat := testcatalog.Geo(testcatalog.Contrib(testutil.FixtureCatalog()))
	names := []string{"near", "trgm", "vec", "latlon", "levenshtein", "geometry", "body_levenshtein_distance"}
	for _, tb := range cat.Tables {
		if tb.Schema == testcatalog.ContribSchema && tb.Name == "docs" {
			for i, n := range names {
				tb.Columns = append(tb.Columns, &introspect.Column{Name: n, Position: 30 + i, PGType: "text", TypeSchema: "pg_catalog"})
			}
		}
	}
	cat.Functions = append(cat.Functions, &introspect.Function{
		Schema: "public", Name: "trgm_probe",
		Args:       []introspect.FuncArg{{Name: "trgm", PGType: "text", TypeSchema: "pg_catalog", Mode: introspect.ArgModeIn}},
		ReturnType: "posts", ReturnTypeSchema: "public", ReturnsSet: true, Volatility: introspect.VolatilityStable,
	})
	b := buildCatalog(t, cat, false, []string{"public", testcatalog.ContribSchema}, nil)
	plain := buildContrib(t, false)

	for _, col := range names {
		p := mustParse(t, b, "/api/kb/docs?"+col+"=eq.x")
		if c, ok := p.Filter.(plan.Cond); !ok || c.Column.Name != col || c.Value != "x" {
			t.Errorf("%s=eq.x filter = %#v", col, p.Filter)
		}
	}
	for _, u := range []string{
		"/api/kb/docs?order=title.trgm_distance&~trgm=x",
		"/api/kb/docs?select=id,~title_trgm_similarity&~trgm=x",
		"/api/kb/docs?select=id,~body_levenshtein_distance,body_levenshtein_distance&body=levenshtein_distance_lt.2&order=body.levenshtein_distance&~levenshtein=Jon",
	} {
		got, err := doParse(t, b, req{url: u}, allOn)
		if err != nil {
			t.Errorf("%s: %v", u, err)
			continue
		}
		want, err := doParse(t, plain, req{url: strings.ReplaceAll(u, ",body_levenshtein_distance&", "&")}, allOn)
		if err != nil {
			t.Errorf("%s without the columns: %v", u, err)
			continue
		}
		if got.Trgm != want.Trgm || got.Levenshtein != want.Levenshtein {
			t.Errorf("%s: references %v/%v, want %v/%v", u, got.Trgm, got.Levenshtein, want.Trgm, want.Levenshtein)
		}
	}
	p := mustParse(t, b, "/api/kb/docs?select=id,~body_levenshtein_distance,body_levenshtein_distance&~levenshtein=Jon")
	if fs := p.Select.Fields; len(fs) != 3 || fs[1].Pseudo == nil || fs[2].Column == nil || fs[2].Column.Name != "body_levenshtein_distance" {
		t.Errorf("pseudo and real column side by side = %+v", fs)
	}
	docs, plainDocs := b.ByPath["kb.docs"], plain.ByPath["kb.docs"]
	if got, want := len(b.ContribOrders(docs, docs.Table.Column("title"))), len(plain.ContribOrders(plainDocs, plainDocs.Table.Column("title"))); got != want {
		t.Errorf("title order terms next to the columns = %d, want %d", got, want)
	}
	seen := map[string]int{}
	for _, prm := range b.OpenAPI.Paths["/kb/docs"].Get.Parameters {
		seen[prm.Name]++
	}
	for _, n := range []string{"trgm", "near", "~trgm", "~levenshtein"} {
		if seen[n] != 1 {
			t.Errorf("%s documented %d times, want once", n, seen[n])
		}
	}

	p = mustParse(t, b, "/api/rpc/trgm_probe?trgm=fuzzy")
	if p.Body.Object["trgm"] != "fuzzy" {
		t.Errorf("rpc arg trgm = %#v", p.Body.Object)
	}

	for url, code := range map[string]string{
		"/api/kb/docs?~trgm=x":                           CodeInvalidParameter,
		"/api/kb/docs?~trgm=x&~trgm=y":                   CodeInvalidParameter,
		"/api/kb/docs?order=search.rank&~trgm=x":         CodeInvalidFilter,
		"/api/places?~near=POINT(1%202)":                 CodeInvalidParameter,
		"/api/places?order=id&~near=POINT(1%202)":        CodeInvalidParameter,
		"/api/kb/docs?~nope=eq.1":                        CodeUnknownColumn,
		"/api/kb/docs?select=~nope":                      CodeUnknownColumn,
		"/api/kb/docs?order=~nope":                       CodeUnknownColumn,
		"/api/kb/docs?select=body_levenshtein_distance":  CodeUnknownColumn,
		"/api/kb/docs?order=title.trgm_distance&~trgm=x": "",
	} {
		_, err := doParse(t, plain, req{url: url}, allOn)
		if code == "" {
			if err != nil {
				t.Errorf("%s: %v", url, err)
			}
			continue
		}
		wantCode(t, err, code)
	}
}

// TestRankFilterNested: a rank term finds its full-text filter inside
// and=(...) too, but not under or= / not.
func TestRankFilterNested(t *testing.T) {
	b := buildContrib(t, false)
	p := mustParse(t, b, "/api/kb/docs?and=(id.gt.1,and(search.fts.cat))&order=search.rank.desc")
	if f := p.Order[0].Contrib.Filter; f == nil || f.Column.Name != "search" {
		t.Fatalf("rank filter = %#v", f)
	}
	for _, q := range []string{"or=(search.fts.cat,id.eq.1)", "not.and=(search.fts.cat)", "search=not.fts.cat", "search=fts.a&and=(search.fts.b)"} {
		_, err := doParse(t, b, req{url: "/api/kb/docs?" + q + "&order=search.rank"}, allOn)
		wantCode(t, err, CodeInvalidFilter)
	}
}

// TestContribJSONArgs: RPC arguments and array elements of hstore, range
// and multirange types take the JSON forms OpenAPI documents, converted to
// the type's literal text.
func TestContribJSONArgs(t *testing.T) {
	cat := testcatalog.Contrib(testutil.FixtureCatalog())
	ext := testcatalog.ContribExtSchema
	cat.Functions = append(cat.Functions, &introspect.Function{
		Schema: "public", Name: "contrib_probe",
		Args: []introspect.FuncArg{
			{Name: "h", PGType: "hstore", TypeSchema: ext, Mode: introspect.ArgModeIn},
			{Name: "hs", PGType: "_hstore", TypeSchema: ext, Mode: introspect.ArgModeIn},
			{Name: "r", PGType: "int4range", TypeSchema: "pg_catalog", Mode: introspect.ArgModeIn},
			{Name: "m", PGType: "datemultirange", TypeSchema: "pg_catalog", Mode: introspect.ArgModeIn},
		},
		ReturnType: "int4", ReturnTypeSchema: "pg_catalog", Volatility: introspect.VolatilityVolatile,
	})
	b := buildCatalog(t, cat, false, []string{"public", testcatalog.ContribSchema}, nil)
	p, err := doParse(t, b, req{method: "POST", url: "/api/rpc/contrib_probe", header: map[string]string{"Content-Type": "application/json"}, body: `{
		"h": {"a": "x \"y\"", "b": null}, "hs": [{"k": "v"}, "c=>d", null],
		"r": {"start": 1, "end": 5, "endInclusive": true},
		"m": [{"start": "2020-01-01", "end": null}, "[2021-01-01,2021-02-01)"]}`}, allOn)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"h":  `"a"=>"x \"y\"", "b"=>NULL`,
		"hs": []any{`"k"=>"v"`, "c=>d", nil},
		"r":  `["1","5"]`,
		"m":  `{["2020-01-01",),["2021-01-01","2021-02-01")}`,
	}
	for k, v := range want {
		if !reflect.DeepEqual(p.Body.Object[k], v) {
			t.Errorf("%s = %#v, want %#v", k, p.Body.Object[k], v)
		}
	}
	for _, bad := range []string{`{"h": {"a": 1}}`, `{"r": {"start": 1.5}}`, `{"m": {"start": 1}}`} {
		_, err := doParse(t, b, req{method: "POST", url: "/api/rpc/contrib_probe", header: map[string]string{"Content-Type": "application/json"}, body: bad}, allOn)
		wantCode(t, err, CodeInvalidArgument)
	}
}

// TestLevenshteinReference: ~levenshtein= binds the reference text for the
// Levenshtein order term, score and threshold.
func TestLevenshteinReference(t *testing.T) {
	plain := buildContrib(t, false)
	p := mustParse(t, plain, "/api/kb/docs?select=id,~body_levenshtein_distance&body=levenshtein_distance_lt.2&order=body.levenshtein_distance&~levenshtein=Jon")
	if p.Levenshtein != "Jon" {
		t.Errorf("Levenshtein = %#v", p.Levenshtein)
	}
}
