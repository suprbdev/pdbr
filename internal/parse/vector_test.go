package parse

import (
	"net/url"
	"reflect"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/plan"
	"github.com/suprbdev/pdbr/internal/testcatalog"
)

// TestVectorOrder: order=<col>.vec_* binds vec= once for the root and every
// embed, decoded from a JSON array or kept as text, and checked against each
// term's column type; the term without vec=, vec= without a term and a
// malformed vector are 400 invalid_filter.
func TestVectorOrder(t *testing.T) {
	b := buildContrib(t, false)
	vec := url.QueryEscape("[0.1,0.2,0.3]")
	p := mustParse(t, b, "/api/kb/embeddings?select=id,chunks(id)&order=emb.vec_cos,id&chunks.order=emb.vec_l2.desc&vec="+vec)
	if co := p.Order[0].Contrib; co == nil || co.Name != "vec_cos" || co.Expr != "cosineDistance" || co.Column.Name != "emb" {
		t.Fatalf("root term = %#v", p.Order[0])
	}
	if co := p.Select.Embeds[0].Order[0].Contrib; co == nil || co.Expr != "l2Distance" || !p.Select.Embeds[0].Order[0].Desc {
		t.Fatalf("embed term = %#v", p.Select.Embeds[0].Order[0])
	}
	if list, ok := p.Vec.([]any); !ok || len(list) != 3 {
		t.Errorf("vec = %#v, want the decoded list", p.Vec)
	}
	p = mustParse(t, b, "/api/kb/embeddings?order=sparse.vec_ip&vec="+url.QueryEscape("{1:1.5}/3"))
	if p.Vec != "{1:1.5}/3" {
		t.Errorf("text vec = %#v", p.Vec)
	}
	mustParse(t, b, "/api/kb/embeddings?order=sparse.vec_l1,half.vec_l2&vec="+vec)

	for q, code := range map[string]string{
		"order=emb.vec_l2":    CodeInvalidFilter,
		"vec=" + vec:          CodeInvalidFilter,
		"order=id&vec=" + vec: CodeInvalidFilter,
		"order=emb.vec_l2&vec=" + url.QueryEscape("[1,"):   CodeInvalidFilter,
		"order=emb.vec_l2&vec=" + url.QueryEscape(`["a"]`): CodeInvalidFilter,
		"order=emb.vec_l2&vec=" + url.QueryEscape("[]"):    CodeInvalidFilter,
		"order=emb.vec_l2&vec=":                            CodeInvalidFilter,
		"order=emb":                                        CodeColumnNotOrderable,
		"order=title.vec_l2&vec=" + vec:                    CodeColumnNotOrderable,
		"order=emb.trgm_dist&trgm=x":                       CodeColumnNotOrderable,
		"distinct=emb":                                     CodeInvalidFilter,
		"emb=eq." + vec:                                    CodeInvalidFilter,
		"emb=in.(" + vec + ")":                             CodeInvalidFilter,
		"emb=not.eq." + vec:                                CodeInvalidFilter,
		"or=(emb.lt.x,id.eq.1)":                            CodeInvalidFilter,
	} {
		_, err := doParse(t, b, req{url: "/api/kb/embeddings?" + q}, allOn)
		if err == nil {
			t.Errorf("%s: parsed", q)
			continue
		}
		wantCode(t, err, code)
	}
	for _, q := range []string{"emb=is.null", "emb=not.is.null", "or=(emb.is.null,id.eq.1)"} {
		mustParse(t, b, "/api/kb/embeddings?"+q)
	}
	_, err := doParse(t, b, req{url: "/api/kb/embeddings?select=title,emb.max()"}, aggOn)
	wantCode(t, err, CodeInvalidSelect)

	indexed := buildContrib(t, true)
	mustParse(t, indexed, "/api/kb/embeddings?order=emb.vec_l1&vec="+vec)
	mustParse(t, indexed, "/api/kb/embeddings?order=half.vec_cos&vec="+vec)
	for _, q := range []string{"order=half.vec_l1&vec=" + vec, "order=sparse.vec_l2&vec=" + vec} {
		_, err := doParse(t, indexed, req{url: "/api/kb/embeddings?" + q}, allOn)
		wantCode(t, err, CodeColumnNotOrderable)
	}
}

// TestVectorSoftReserved: where a column or RPC argument is named vec, vec=
// is that filter or argument and the vector order terms are unavailable.
func TestVectorSoftReserved(t *testing.T) {
	cat := testcatalog.Contrib(testutil.FixtureCatalog())
	emb := cat.Table(testcatalog.ContribSchema, "embeddings")
	emb.Columns = append(emb.Columns, &introspect.Column{Name: "vec", Position: 6, PGType: "text", TypeSchema: "pg_catalog"})
	b := buildCatalog(t, cat, false, []string{"public", testcatalog.ContribSchema}, nil)
	p := mustParse(t, b, "/api/kb/embeddings?vec=eq.x")
	if c, ok := p.Filter.(plan.Cond); !ok || c.Column.Name != "vec" {
		t.Errorf("vec=eq.x filter = %#v", p.Filter)
	}
	_, err := doParse(t, b, req{url: "/api/kb/embeddings?order=emb.vec_l2&vec=eq.x"}, allOn)
	wantCode(t, err, CodeColumnNotOrderable)
	_, err = doParse(t, b, req{url: "/api/kb/chunks?order=emb.vec_l2&vec=" + url.QueryEscape("[1]")}, allOn)
	if err != nil {
		t.Errorf("chunks has no vec column: %v", err)
	}
}

// TestVectorBody: vector bodies and RPC arguments take a list of numbers or
// the text form; anything else is 400 invalid_body.
func TestVectorBody(t *testing.T) {
	cat := testcatalog.Contrib(testutil.FixtureCatalog())
	ext := testcatalog.ContribExtSchema
	cat.Functions = append(cat.Functions, &introspect.Function{
		Schema: "public", Name: "vector_probe",
		Args: []introspect.FuncArg{
			{Name: "v", PGType: "vector", TypeSchema: ext, Mode: introspect.ArgModeIn},
			{Name: "s", PGType: "sparsevec", TypeSchema: ext, Mode: introspect.ArgModeIn},
			{Name: "vs", PGType: "_vector", TypeSchema: ext, Mode: introspect.ArgModeIn},
		},
		ReturnType: "int4", ReturnTypeSchema: "pg_catalog", Volatility: introspect.VolatilityVolatile,
	})
	b := buildCatalog(t, cat, false, []string{"public", testcatalog.ContribSchema}, nil)
	json := map[string]string{"Content-Type": "application/json"}
	p, err := doParse(t, b, req{method: "POST", url: "/api/rpc/vector_probe", header: json,
		body: `{"v": [1, 2.5, -3e-2], "s": [0, 1.5, 0, 2], "vs": [[1, 2], "[3,4]", null]}`}, allOn)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"v": "[1,2.5,-0.03]", "s": "{2:1.5,4:2}/4", "vs": []any{"[1,2]", "[3,4]", nil}}
	for k, v := range want {
		if !reflect.DeepEqual(p.Body.Object[k], v) {
			t.Errorf("%s = %#v, want %#v", k, p.Body.Object[k], v)
		}
	}

	p, err = doParse(t, b, req{method: "POST", url: "/api/kb/embeddings", header: json,
		body: `{"id": 1, "emb": [1, 2, 3], "half": "[1,2]", "sparse": null}`}, allOn)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Body.Object["emb"].([]any); !ok || p.Body.Object["half"] != "[1,2]" {
		t.Errorf("body = %#v", p.Body.Object)
	}
	for _, bad := range []string{`{"emb": [1, "x"]}`, `{"emb": []}`, `{"emb": {"a": 1}}`, `{"emb": 1}`, `{"emb": true}`} {
		_, err := doParse(t, b, req{method: "POST", url: "/api/kb/embeddings", header: json, body: bad}, allOn)
		wantCode(t, err, CodeInvalidBody)
	}
	_, err = doParse(t, b, req{method: "POST", url: "/api/rpc/vector_probe", header: json, body: `{"v": [1, "x"]}`}, allOn)
	wantCode(t, err, CodeInvalidBody)
}

// TestUnaccentFilters: the unaccent operators parse with not. and inside
// logic lists; under filters.indexed_only they need filters.allow_columns.
func TestUnaccentFilters(t *testing.T) {
	b := buildContrib(t, false)
	p := mustParse(t, b, "/api/kb/docs?title=not.unaccent_eq.Cafe&or=(body.unaccent_ilike.*creme*,email.unaccent_sw.Zoe,title.unaccent_includes.50%25)")
	and, ok := p.Filter.(plan.And)
	if !ok || len(and.Terms) != 2 {
		t.Fatalf("filter = %#v", p.Filter)
	}
	if c := and.Terms[0].(plan.Cond); !c.Negate || c.Op != "unaccent_eq" || c.Value != "Cafe" {
		t.Errorf("not.unaccent_eq = %#v", c)
	}
	if or := and.Terms[1].(plan.Or); len(or.Terms) != 3 || or.Terms[0].(plan.Cond).Value != "*creme*" || or.Terms[2].(plan.Cond).Value != "50%" {
		t.Errorf("or = %#v", and.Terms[1])
	}
	for _, q := range []string{"id=unaccent_eq.1", "attrs=unaccent_eq.x", "title=unaccent_eq(any).{a,b}"} {
		_, err := doParse(t, b, req{url: "/api/kb/docs?" + q}, allOn)
		if err == nil {
			t.Errorf("%s parsed", q)
		}
	}
	indexed := buildContrib(t, true)
	mustParse(t, indexed, "/api/kb/docs?body=unaccent_eq.x")
	_, err := doParse(t, indexed, req{url: "/api/kb/docs?title=unaccent_eq.x"}, allOn)
	wantCode(t, err, CodeColumnNotFilterable)
}
