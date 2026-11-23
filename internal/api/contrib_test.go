package api

import (
	"bytes"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/testcatalog"
)

func openAPIJSON(t *testing.T, b *Built) []byte {
	t.Helper()
	out, err := b.OpenAPI.JSON()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func contribOptions(indexedOnly bool) Options {
	opts := defaultOptions()
	opts.FilterIndexedOnly = indexedOnly
	opts.Schemas = []string{"public", testcatalog.ContribSchema}
	opts.AllowColumns = map[string][]string{testcatalog.ContribSchema + ".docs": {"body"}}
	return opts
}

// TestContribGatedOnExtensions: an installed extension no column uses
// changes nothing, pg_trgm adds its operators to text columns only when it
// is installed, and extension-typed columns without the extension record get
// no extension operators.
func TestContribGatedOnExtensions(t *testing.T) {
	opts := defaultOptions()
	opts.FilterIndexedOnly = false
	plain := openAPIJSON(t, build(t, testutil.FixtureCatalog(), opts))

	cat := testutil.FixtureCatalog()
	for _, name := range []string{"citext", "hstore", "ltree"} {
		cat.Extensions = append(cat.Extensions, &introspect.Extension{Name: name, Version: "1.0", Schema: "ext"})
	}
	if got := openAPIJSON(t, build(t, cat, opts)); !bytes.Equal(got, plain) {
		t.Error("installing citext/hstore/ltree without columns of those types changed the OpenAPI document")
	}

	cat.Extensions = append(cat.Extensions, &introspect.Extension{Name: "pg_trgm", Version: "1.6", Schema: "ext"})
	b := build(t, cat, opts)
	users := b.ByPath["users"]
	if ops := b.FilterOps(users, users.Table.Column("email")); !slices.Contains(ops, "trgm_sim") || !slices.Contains(ops, "trgm_swsim") {
		t.Errorf("email ops with pg_trgm = %v", ops)
	}
	if ops := b.FilterOps(users, users.Table.Column("id")); slices.Contains(ops, "trgm_sim") {
		t.Errorf("int column got trigram ops: %v", ops)
	}
	cat.Extensions[len(cat.Extensions)-1].Version = "1.1"
	b = build(t, cat, opts)
	if ops := b.FilterOps(users, users.Table.Column("email")); !slices.Contains(ops, "trgm_sim") || slices.Contains(ops, "trgm_wsim") {
		t.Errorf("pg_trgm 1.1 ops = %v, want trgm_sim without the word operators", ops)
	}

	bare := testcatalog.Contrib(testutil.FixtureCatalog())
	bare.Extensions = bare.Extensions[:2]
	b = build(t, bare, contribOptions(false))
	docs := b.ByPath["kb.docs"]
	for col, want := range map[string][]string{
		"attrs": {"eq", "neq", "gt", "gte", "lt", "lte", "in", "is", "isdistinct"},
		"path":  {"eq", "neq", "gt", "gte", "lt", "lte", "in", "is", "isdistinct"},
		"email": {"eq", "neq", "gt", "gte", "lt", "lte", "in", "is", "isdistinct", "like", "ilike", "sw", "ew", "match", "imatch"},
	} {
		if got := b.FilterOps(docs, docs.Table.Column(col)); !reflect.DeepEqual(got, want) {
			t.Errorf("%s ops without its extension = %v, want %v", col, got, want)
		}
	}
}

func TestContribFilterOps(t *testing.T) {
	cases := map[string][]string{
		"title":  {"eq", "neq", "gt", "gte", "lt", "lte", "in", "is", "isdistinct", "like", "ilike", "sw", "ew", "match", "imatch", "trgm_sim", "trgm_wsim", "trgm_swsim"},
		"body":   {"eq", "neq", "gt", "gte", "lt", "lte", "in", "is", "isdistinct", "like", "ilike", "sw", "ew", "match", "imatch", "fts", "plfts", "phfts", "wfts", "trgm_sim", "trgm_wsim", "trgm_swsim"},
		"search": {"eq", "neq", "gt", "gte", "lt", "lte", "in", "is", "isdistinct", "fts", "plfts", "phfts", "wfts"},
		"email":  {"eq", "neq", "gt", "gte", "lt", "lte", "in", "is", "isdistinct", "like", "ilike", "sw", "ew", "match", "imatch"},
		"attrs":  {"eq", "neq", "in", "is", "isdistinct", "hk", "hk_all", "hk_any", "cs"},
		"path":   {"eq", "neq", "gt", "gte", "lt", "lte", "in", "is", "isdistinct", "cs", "cd", "ancestor", "descendant", "lquery", "lquery_any", "ltxtquery"},
		"paths":  {"eq", "neq", "gt", "gte", "lt", "lte", "is", "isdistinct", "cs", "cd", "ov", "descendant", "lquery", "lquery_any", "ltxtquery"},
		"pages":  {"eq", "neq", "gt", "gte", "lt", "lte", "in", "is", "isdistinct", "ov", "cs", "cd", "sl", "sr", "nxr", "nxl", "adj"},
		"slots":  {"eq", "neq", "gt", "gte", "lt", "lte", "in", "is", "isdistinct", "ov", "cs", "cd", "sl", "sr", "nxr", "nxl", "adj"},
		"net":    {"eq", "neq", "gt", "gte", "lt", "lte", "in", "is", "isdistinct", "cs", "cd", "ov", "cs_strict", "cd_strict"},
	}
	b := build(t, testcatalog.Contrib(testutil.FixtureCatalog()), contribOptions(true))
	docs := b.ByPath["kb.docs"]
	for col, want := range cases {
		if got := b.FilterOps(docs, docs.Table.Column(col)); !reflect.DeepEqual(got, want) {
			t.Errorf("%s ops = %v\nwant %v", col, got, want)
		}
	}
	get := b.OpenAPI.Paths["/kb/docs"].Get
	if p := paramByName(get.Parameters, "attrs"); p == nil || !reflect.DeepEqual(p.XFilterOps, cases["attrs"]) {
		t.Errorf("attrs param = %+v", p)
	}
	order := paramByName(get.Parameters, "order").Description
	for _, want := range []string{"body.rank, body.rank_cd, search.rank, search.rank_cd", "title.trgm_dist, title.trgm_wdist, title.trgm_swdist, body.trgm_dist"} {
		if !strings.Contains(order, want) {
			t.Errorf("order description lacks %q: %s", want, order)
		}
	}
	if paramByName(get.Parameters, "trgm") == nil {
		t.Error("kb.docs lacks the trgm param")
	}
	if paramByName(b.OpenAPI.Paths["/users"].Get.Parameters, "trgm") != nil {
		t.Error("users documents trgm without a trigram-orderable column")
	}

	schemas := b.OpenAPI.Components.Schemas
	if s := schemas["kb.docs"].Properties["attrs"]; !reflect.DeepEqual(s.Type, []string{"object", "null"}) {
		t.Errorf("attrs output schema = %+v", s)
	}
	if s := schemas["kb.docs"].Properties["pages"]; !reflect.DeepEqual(s.Type, []string{"string", "null"}) {
		t.Errorf("pages output schema = %+v, want the range literal text", s)
	}
	if s := schemas["kb.docs.insert"].Properties["pages"]; len(s.AnyOf) != 2 || len(s.AnyOf[0].OneOf) != 2 || s.AnyOf[0].OneOf[1].Properties["startInclusive"] == nil {
		t.Errorf("pages input schema = %+v", s)
	}
	if s := schemas["kb.docs.insert"].Properties["slots"]; len(s.AnyOf) != 2 || s.AnyOf[0].OneOf[1].Type != "array" {
		t.Errorf("slots input schema = %+v", s)
	}
}

// TestContribCustomRanges: a range type recorded in Catalog.Ranges (CREATE
// TYPE ... AS RANGE) and its multirange get the range operators and the
// structured input schema, like the built-in ranges; a same-named type the
// catalog does not record as a range gets neither.
func TestContribCustomRanges(t *testing.T) {
	cat := testcatalog.Contrib(testutil.FixtureCatalog())
	cat.Ranges = append(cat.Ranges, &introspect.RangeType{
		Schema: testcatalog.ContribSchema, Name: "floatrange", Subtype: "float8", SubtypeSchema: "pg_catalog",
		Multirange: "floatmultirange", MultirangeSchema: testcatalog.ContribSchema,
	})
	docs := cat.Table(testcatalog.ContribSchema, "docs")
	custom := func(name string, pos int, typ, schema string) *introspect.Column {
		return &introspect.Column{Name: name, Position: pos, PGType: typ, TypeSchema: schema, BaseTypeSchema: schema}
	}
	docs.Columns = append(docs.Columns,
		custom("score", 13, "floatrange", testcatalog.ContribSchema),
		custom("scores", 14, "floatmultirange", testcatalog.ContribSchema),
		custom("fake", 15, "floatrange", "public"),
	)
	opts := contribOptions(false)
	b := build(t, cat, opts)
	res := b.ByPath["kb.docs"]
	rangeOps := []string{"eq", "neq", "gt", "gte", "lt", "lte", "in", "is", "isdistinct", "ov", "cs", "cd", "sl", "sr", "nxr", "nxl", "adj"}
	for _, col := range []string{"score", "scores"} {
		if got := b.FilterOps(res, res.Table.Column(col)); !reflect.DeepEqual(got, rangeOps) {
			t.Errorf("%s ops = %v\nwant %v", col, got, rangeOps)
		}
		if p := paramByName(b.OpenAPI.Paths["/kb/docs"].Get.Parameters, col); p == nil || !reflect.DeepEqual(p.XFilterOps, rangeOps) {
			t.Errorf("%s param = %+v", col, p)
		}
	}
	if got := b.FilterOps(res, res.Table.Column("fake")); slices.Contains(got, "ov") {
		t.Errorf("public.floatrange is not a recorded range but got %v", got)
	}
	schemas := b.OpenAPI.Components.Schemas
	if s := schemas["kb.docs.insert"].Properties["score"]; len(s.AnyOf) != 2 || len(s.AnyOf[0].OneOf) != 2 || s.AnyOf[0].OneOf[1].Properties["startInclusive"] == nil {
		t.Errorf("score input schema = %+v", s)
	}
	if s := schemas["kb.docs.insert"].Properties["scores"]; len(s.AnyOf) != 2 || s.AnyOf[0].OneOf[1].Type != "array" {
		t.Errorf("scores input schema = %+v", s)
	}
	if s := schemas["kb.docs.insert"].Properties["fake"]; len(s.AnyOf) == 2 && len(s.AnyOf[0].OneOf) == 2 {
		t.Errorf("fake input schema = %+v, want the plain literal", s)
	}
}
