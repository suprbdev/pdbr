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
		"title": {"eq", "neq", "gt", "gte", "lt", "lte", "in", "is", "isdistinct", "like", "ilike", "sw", "ew", "match", "imatch", "trgm_sim", "trgm_wsim", "trgm_swsim"},
		"body": {"eq", "neq", "gt", "gte", "lt", "lte", "in", "is", "isdistinct", "like", "ilike", "sw", "ew", "match", "imatch", "fts", "plfts", "phfts", "wfts", "trgm_sim", "trgm_wsim", "trgm_swsim", "unaccent_eq", "unaccent_ilike", "unaccent_sw", "unaccent_includes",
			"levenshtein_lt", "soundex", "metaphone", "dmetaphone"},
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

// TestContribUnaccent: the unaccent operators follow the other text
// operators on text-like columns when the extension is installed, and no
// index serves them, so filters.indexed_only admits them only through
// filters.allow_columns.
func TestContribUnaccent(t *testing.T) {
	unaccent := []string{"unaccent_eq", "unaccent_ilike", "unaccent_sw", "unaccent_includes"}
	b := build(t, testcatalog.Contrib(testutil.FixtureCatalog()), contribOptions(false))
	docs := b.ByPath["kb.docs"]
	for _, col := range []string{"title", "body", "email"} {
		if got := b.FilterOps(docs, docs.Table.Column(col)); !reflect.DeepEqual(got[len(got)-8:len(got)-4], unaccent) {
			t.Errorf("%s ops = %v, want the unaccent operators before the fuzzystrmatch ones", col, got)
		}
	}
	for _, col := range []string{"id", "attrs", "path", "pages"} {
		if got := b.FilterOps(docs, docs.Table.Column(col)); slices.Contains(got, "unaccent_eq") {
			t.Errorf("%s got unaccent ops: %v", col, got)
		}
	}
	b = build(t, testcatalog.Contrib(testutil.FixtureCatalog()), contribOptions(true))
	docs = b.ByPath["kb.docs"]
	if got := b.FilterOps(docs, docs.Table.Column("title")); slices.Contains(got, "unaccent_eq") {
		t.Errorf("indexed title (gist) admits unaccent ops: %v", got)
	}
	if got := b.FilterOps(docs, docs.Table.Column("body")); !slices.Contains(got, "unaccent_includes") {
		t.Errorf("allow-listed body lacks unaccent ops: %v", got)
	}

	cat := testcatalog.Contrib(testutil.FixtureCatalog())
	var exts []*introspect.Extension
	for _, e := range cat.Extensions {
		if e.Name != "unaccent" {
			exts = append(exts, e)
		}
	}
	cat.Extensions = exts
	b = build(t, cat, contribOptions(false))
	docs = b.ByPath["kb.docs"]
	if got := b.FilterOps(docs, docs.Table.Column("body")); slices.Contains(got, "unaccent_eq") {
		t.Errorf("unaccent ops without the extension: %v", got)
	}
}

// TestContribVector: pgvector columns take is only, order by the distances
// their indexes serve (any distance without the policy), document vec=,
// and are arrays of numbers (sparsevec: text) in the schemas.
func TestContribVector(t *testing.T) {
	names := func(os []ContribOrder) []string {
		var out []string
		for _, o := range os {
			out = append(out, o.Name)
		}
		return out
	}
	all := []string{"vec_l2", "vec_ip", "vec_cos", "vec_l1"}
	for _, tc := range []struct {
		indexedOnly bool
		col         string
		want        []string
	}{
		{false, "emb", all},
		{false, "half", all},
		{false, "sparse", all},
		{true, "emb", all},
		{true, "half", []string{"vec_l2", "vec_ip", "vec_cos"}},
		{true, "sparse", nil},
	} {
		b := build(t, testcatalog.Contrib(testutil.FixtureCatalog()), contribOptions(tc.indexedOnly))
		res := b.ByPath["kb.embeddings"]
		c := res.Table.Column(tc.col)
		if got := names(b.ContribOrders(res, c)); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("indexed_only=%v %s orders = %v, want %v", tc.indexedOnly, tc.col, got, tc.want)
		}
		if res.FilterColumns[tc.col] {
			want := []string{"is"}
			if !tc.indexedOnly {
				want = append(want, "vec_l2_lt", "vec_ip_lt", "vec_cos_lt", "vec_l1_lt")
			}
			if got := b.FilterOps(res, c); !reflect.DeepEqual(got, want) {
				t.Errorf("indexed_only=%v %s ops = %v, want %v", tc.indexedOnly, tc.col, got, want)
			}
		}
	}

	b := build(t, testcatalog.Contrib(testutil.FixtureCatalog()), contribOptions(true))
	get := b.OpenAPI.Paths["/kb/embeddings"].Get
	if paramByName(get.Parameters, "vec") == nil {
		t.Error("kb.embeddings lacks the vec param")
	}
	if paramByName(b.OpenAPI.Paths["/kb/docs"].Get.Parameters, "vec") != nil {
		t.Error("kb.docs documents vec without a vector column")
	}
	order := paramByName(get.Parameters, "order").Description
	if !strings.Contains(order, "emb.vec_l2, emb.vec_ip, emb.vec_cos, emb.vec_l1, half.vec_l2, half.vec_ip, half.vec_cos, code.vec_hamming, code.vec_jaccard.") {
		t.Errorf("order description: %s", order)
	}
	if !strings.Contains(order, "Orderable columns: id, code.") {
		t.Errorf("order lists a vector column as plainly orderable: %s", order)
	}

	schemas := b.OpenAPI.Components.Schemas
	out := schemas["kb.embeddings"].Properties
	if s := out["emb"]; !reflect.DeepEqual(s.Type, []string{"array", "null"}) || s.Items == nil || s.Items.Type != "number" {
		t.Errorf("emb output schema = %+v", s)
	}
	if s := out["sparse"]; !reflect.DeepEqual(s.Type, []string{"string", "null"}) {
		t.Errorf("sparse output schema = %+v", s)
	}
	in := schemas["kb.embeddings.insert"].Properties
	for _, col := range []string{"emb", "half", "sparse"} {
		if s := in[col]; len(s.AnyOf) != 2 || len(s.AnyOf[0].OneOf) != 2 {
			t.Errorf("%s input schema = %+v", col, s)
		}
	}

	cat := testcatalog.Contrib(testutil.FixtureCatalog())
	cat.Table(testcatalog.ContribSchema, "embeddings").Columns = append(cat.Table(testcatalog.ContribSchema, "embeddings").Columns,
		&introspect.Column{Name: "vec", Position: 6, PGType: "int4", TypeSchema: "pg_catalog"})
	b = build(t, cat, contribOptions(false))
	res := b.ByPath["kb.embeddings"]
	if got := b.ContribOrders(res, res.Table.Column("emb")); len(got) != 0 {
		t.Errorf("vector orders with a column named vec = %v", names(got))
	}
	if paramByName(b.OpenAPI.Paths["/kb/embeddings"].Get.Parameters, "vec").XFilterOps == nil {
		t.Error("the vec column lost its filter parameter")
	}
}

// TestContribIndexOpClasses: under indexed_only, an index serves a contrib
// operator or ordering only when its operator class matches the hint: a
// btree_gist GiST index on inet serves none of the inet operators, and an
// HNSW index serves only the metric of its operator class.
func TestContribIndexOpClasses(t *testing.T) {
	withOpClass := func(table, index, opClass string) *introspect.Catalog {
		cat := testcatalog.Contrib(testutil.FixtureCatalog())
		for _, ix := range cat.Table(testcatalog.ContribSchema, table).Indexes {
			if ix.Name == index {
				ix.OpClasses = []string{opClass}
				return cat
			}
		}
		t.Fatalf("index %s missing", index)
		return nil
	}
	for opClass, want := range map[string]bool{"inet_ops": true, "gist_inet_ops": false} {
		b := build(t, withOpClass("docs", "docs_net_idx", opClass), contribOptions(true))
		docs := b.ByPath["kb.docs"]
		if got := slices.Contains(b.FilterOps(docs, docs.Table.Column("net")), "cs_strict"); got != want {
			t.Errorf("net led by a %s index: cs_strict offered = %v, want %v", opClass, got, want)
		}
	}

	b := build(t, withOpClass("embeddings", "embeddings_emb_idx", "vector_cosine_ops"), contribOptions(true))
	res := b.ByPath["kb.embeddings"]
	var got []string
	for _, o := range b.ContribOrders(res, res.Table.Column("emb")) {
		got = append(got, o.Name)
	}
	if !reflect.DeepEqual(got, []string{"vec_cos"}) {
		t.Errorf("emb led by a vector_cosine_ops index: orders = %v, want [vec_cos]", got)
	}
}
