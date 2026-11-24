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

// TestReservedColumnNames: a column whose key starts with api.ReservedPrefix
// is returned by select=* and written through the body, but no query-string
// position names it: plain, cast, JSON-path and aggregate select items,
// filters, every order form, distinct=, on_conflict= and columns= answer as
// they do for an unknown ~ name.
func TestReservedColumnNames(t *testing.T) {
	cat := testutil.FixtureCatalog()
	id := &introspect.Column{Name: "id", Position: 1, PGType: "int4", TypeSchema: "pg_catalog", NotNull: true}
	cat.Tables = append(cat.Tables, &introspect.Table{
		Schema: "public", Name: "tilde", Kind: introspect.RelTable,
		Columns: []*introspect.Column{id,
			{Name: "~x", Position: 2, PGType: "text", TypeSchema: "pg_catalog"},
			{Name: "~j", Position: 3, PGType: "jsonb", TypeSchema: "pg_catalog"},
			{Name: "~u", Position: 4, PGType: "text", TypeSchema: "pg_catalog"}},
		PrimaryKey: &introspect.Constraint{Name: "tilde_pkey", Columns: []string{"id"}},
		Uniques:    []*introspect.Constraint{{Name: "tilde_u_key", Columns: []string{"~u"}}},
		Privileges: introspect.Privileges{Select: true, Insert: true, Update: true, Delete: true},
	})
	b := buildCatalog(t, cat, false, []string{"public"}, nil)
	opts := allOn
	opts.Aggregates = true

	p := mustParse(t, b, "/api/tilde?select=*")
	if !p.Select.Star {
		t.Errorf("select=* = %+v", p.Select)
	}
	json := map[string]string{"Content-Type": "application/json"}
	if p, err := doParse(t, b, req{method: "POST", url: "/api/tilde", header: json, body: `{"id": 1, "~x": "a"}`}, opts); err != nil || p.Body.Object["~x"] != "a" {
		t.Errorf("body write of ~x = %v, %v", p, err)
	}

	for url, code := range map[string]string{
		"/api/tilde?select=~x":              CodeUnknownColumn,
		"/api/tilde?select=~x::text":        CodeUnknownColumn,
		"/api/tilde?select=x:~x::text":      CodeUnknownColumn,
		"/api/tilde?select=~j->a":           CodeUnknownColumn,
		"/api/tilde?select=~j->>a::int":     CodeUnknownColumn,
		"/api/tilde?select=~x.max()":        CodeUnknownColumn,
		"/api/tilde?select=~x.count()":      CodeUnknownColumn,
		"/api/tilde?select=~x::text.max()":  CodeUnknownColumn,
		"/api/tilde?select=~j->a.count()":   CodeUnknownColumn,
		"/api/tilde?~x=eq.a":                CodeUnknownColumn,
		"/api/tilde?~j->>a=eq.1":            CodeUnknownColumn,
		"/api/tilde?or=(~x.eq.a,id.eq.1)":   CodeUnknownColumn,
		"/api/tilde?order=~x.desc":          CodeUnknownColumn,
		"/api/tilde?order=~j->a":            CodeUnknownColumn,
		"/api/tilde?order=~j->>a.desc":      CodeUnknownColumn,
		"/api/tilde?distinct=~x":            CodeUnknownColumn,
		"/api/tilde?select=id&distinct=~x":  CodeUnknownColumn,
		"/api/tilde?select=id,~x":           CodeUnknownColumn,
		"/api/tilde?select=id&order=~u.asc": CodeUnknownColumn,
	} {
		_, err := doParse(t, b, req{url: url}, opts)
		if err == nil {
			t.Errorf("%s: accepted", url)
			continue
		}
		wantCode(t, err, code)
	}
	for _, r := range []struct {
		r    req
		code string
	}{
		{req{method: "PUT", url: "/api/tilde?on_conflict=~u", header: json, body: `[{"id": 1, "~u": "a"}]`}, CodeUnknownColumn},
		{req{method: "POST", url: "/api/tilde?columns=id,~x", header: json, body: `{"id": 1, "~x": "a"}`}, CodeInvalidBody},
	} {
		_, err := doParse(t, b, r.r, opts)
		if err == nil {
			t.Errorf("%s %s: accepted", r.r.method, r.r.url)
			continue
		}
		wantCode(t, err, r.code)
	}
}
