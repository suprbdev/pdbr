package parse

import (
	"reflect"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/plan"
	"github.com/suprbdev/pdbr/internal/testcatalog"
)

// TestSoftReservedParams: near= and trgm= are reserved only where no column
// or RPC argument carries the name; there the name is a filter (or the
// argument), and the order terms needing the parameter are unavailable.
// Elsewhere a stray near=/trgm= is a 400 rather than silently ignored.
func TestSoftReservedParams(t *testing.T) {
	cat := testcatalog.Contrib(testutil.FixtureCatalog())
	for _, tb := range cat.Tables {
		if tb.Schema == testcatalog.ContribSchema && tb.Name == "docs" {
			tb.Columns = append(tb.Columns,
				&introspect.Column{Name: "trgm", Position: 13, PGType: "text", TypeSchema: "pg_catalog"},
				&introspect.Column{Name: "near", Position: 14, PGType: "text", TypeSchema: "pg_catalog"})
		}
	}
	cat.Functions = append(cat.Functions, &introspect.Function{
		Schema: "public", Name: "trgm_probe",
		Args:       []introspect.FuncArg{{Name: "trgm", PGType: "text", TypeSchema: "pg_catalog", Mode: introspect.ArgModeIn}},
		ReturnType: "posts", ReturnTypeSchema: "public", ReturnsSet: true, Volatility: introspect.VolatilityStable,
	})
	b := buildCatalog(t, cat, false, []string{"public", testcatalog.ContribSchema}, nil)

	for _, col := range []string{"trgm", "near"} {
		p := mustParse(t, b, "/api/kb/docs?"+col+"=eq.x")
		if c, ok := p.Filter.(plan.Cond); !ok || c.Column.Name != col || c.Value != "x" {
			t.Errorf("%s=eq.x filter = %#v", col, p.Filter)
		}
	}
	_, err := doParse(t, b, req{url: "/api/kb/docs?order=title.trgm_dist&trgm=eq.x"}, allOn)
	wantCode(t, err, CodeColumnNotOrderable)
	docs := b.ByPath["kb.docs"]
	for _, o := range b.ContribOrders(docs, docs.Table.Column("title")) {
		if o.Param == "trgm" {
			t.Errorf("trigram order %s offered next to a column named trgm", o.Name)
		}
	}
	seen := map[string]int{}
	for _, prm := range b.OpenAPI.Paths["/kb/docs"].Get.Parameters {
		seen[prm.Name]++
	}
	if seen["trgm"] != 1 || seen["near"] != 1 {
		t.Errorf("trgm/near documented %d/%d times, want once each (the filter)", seen["trgm"], seen["near"])
	}

	p := mustParse(t, b, "/api/rpc/trgm_probe?trgm=fuzzy")
	if p.Body.Object["trgm"] != "fuzzy" {
		t.Errorf("rpc arg trgm = %#v", p.Body.Object)
	}

	plain := buildContrib(t, false)
	for url, code := range map[string]string{
		"/api/kb/docs?trgm=x":                       CodeInvalidParameter,
		"/api/kb/docs?order=search.rank&trgm=x":     CodeInvalidFilter,
		"/api/places?near=POINT(1%202)":             CodeInvalidParameter,
		"/api/places?order=id&near=POINT(1%202)":    CodeInvalidParameter,
		"/api/kb/docs?order=title.trgm_dist&trgm=x": "",
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

// TestLevenshteinSoftReserved: levenshtein= yields to a column of that
// name, which then takes the Levenshtein order term, score and threshold
// with it; elsewhere it binds the reference text.
func TestLevenshteinSoftReserved(t *testing.T) {
	cat := testcatalog.Contrib(testutil.FixtureCatalog())
	docs := cat.Table(testcatalog.ContribSchema, "docs")
	docs.Columns = append(docs.Columns, &introspect.Column{Name: "levenshtein", Position: 13, PGType: "text", TypeSchema: "pg_catalog"})
	b := buildCatalog(t, cat, false, []string{"public", testcatalog.ContribSchema}, nil)
	p := mustParse(t, b, "/api/kb/docs?levenshtein=eq.x")
	if c, ok := p.Filter.(plan.Cond); !ok || c.Column.Name != "levenshtein" {
		t.Errorf("levenshtein=eq.x filter = %#v", p.Filter)
	}
	for _, u := range []string{"/api/kb/docs?order=body.levenshtein", "/api/kb/docs?body=levenshtein_lt.2"} {
		_, err := doParse(t, b, req{url: u}, allOn)
		if err == nil {
			t.Errorf("%s parsed next to a column named levenshtein", u)
		}
	}
	if b.ByPath["kb.docs"].Pseudo["body_levenshtein_distance"] != nil {
		t.Error("Levenshtein score next to a column named levenshtein")
	}

	plain := buildContrib(t, false)
	p = mustParse(t, plain, "/api/kb/docs?select=id,body_levenshtein_distance&body=levenshtein_lt.2&order=body.levenshtein&levenshtein=Jon")
	if p.Levenshtein != "Jon" {
		t.Errorf("Levenshtein = %#v", p.Levenshtein)
	}
}
