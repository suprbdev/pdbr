package compile_test

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"
	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/inflect"
	"github.com/suprbdev/pdbr/internal/plan"
)

// rpcCatalog is the fixture plus functions covering the argument and return
// kinds the fixture lacks: composite / composite-array / spatial / int8 /
// json arguments, int8 and composite returns, SETOF composite (jwt).
func rpcCatalog() *introspect.Catalog {
	pg := "pg_catalog"
	cat := testutil.FixtureCatalog()
	cat.Functions = append(cat.Functions,
		&introspect.Function{Schema: "public", Name: "big", Args: []introspect.FuncArg{{Name: "n", PGType: "int8", TypeSchema: pg}}, ReturnType: "int8", ReturnTypeSchema: pg, Volatility: introspect.VolatilityImmutable},
		&introspect.Function{Schema: "public", Name: "home", Args: []introspect.FuncArg{{Name: "a", PGType: "address", TypeSchema: "public"}, {Name: "prev", PGType: "_address", TypeSchema: "public"}}, ReturnType: "address", ReturnTypeSchema: "public", Volatility: introspect.VolatilityStable},
		&introspect.Function{Schema: "public", Name: "near_me", Args: []introspect.FuncArg{{Name: "p", PGType: "geometry", TypeSchema: "public"}, {Name: "opts", PGType: "jsonb", TypeSchema: pg}, {Name: "moods", PGType: "_mood", TypeSchema: "public"}}, ReturnType: "geometry", ReturnTypeSchema: "public", Volatility: introspect.VolatilityStable},
		&introspect.Function{Schema: "public", Name: "all_tokens", ReturnType: "jwt", ReturnTypeSchema: "public", ReturnsSet: true, Volatility: introspect.VolatilityStable},
		&introspect.Function{Schema: "public", Name: "addresses", ReturnType: "address", ReturnTypeSchema: "public", ReturnsSet: true, Volatility: introspect.VolatilityStable},
		&introspect.Function{Schema: "public", Name: "noop", ReturnType: "void", ReturnTypeSchema: pg, Volatility: introspect.VolatilityVolatile},
	)
	return cat
}

func rpcFixture(t *testing.T) *fixture {
	t.Helper()
	b := api.New(rpcCatalog(), inflect.Default, api.Options{
		FilterIndexedOnly: true, Functions: true, BasePath: "/api", Schemas: []string{"public"},
		MaxPageSize: 100, Etags: true, JWTType: "public.jwt", Version: "test",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, built: built, c: compile.New(built)}
}

func rpcReq(f *fixture, name string, args map[string]any) *plan.Request {
	rpc := f.built.RPCByPath[name]
	if rpc == nil {
		f.t.Fatalf("no rpc %q", name)
	}
	if args == nil {
		args = map[string]any{}
	}
	return &plan.Request{Method: plan.POST, RPC: rpc, Body: &plan.Body{Object: args}, Page: plan.Page{Limit: 100}}
}

func TestRPCScalarArgsAndNulls(t *testing.T) {
	f := rpcFixture(t)
	st := f.compile(rpcReq(f, "add_numbers", map[string]any{"a": int64(2)}))
	wantSQL(t, st, "SELECT to_jsonb(__fn.v) AS data\nFROM (SELECT \"public\".\"add_numbers\"($1::\"int4\", NULL::\"int4\") AS v) AS __fn", int64(2))
	if st.Shape != compile.ShapeScalar || !st.Mutation {
		t.Errorf("shape %s mutation %v", st.Shape, st.Mutation)
	}
	st = f.compile(rpcReq(f, "add_numbers", map[string]any{"a": nil, "b": int64(1)}))
	if !strings.Contains(st.SQL, `(NULL::"int4", $1::"int4")`) {
		t.Errorf("explicit null should bind as NULL literal: %s", st.SQL)
	}
	st = f.compile(rpcReq(f, "big", map[string]any{"n": "9007199254740993"}))
	wantSQL(t, st, "SELECT to_jsonb((__fn.v)::text) AS data\nFROM (SELECT \"public\".\"big\"($1::\"int8\") AS v) AS __fn", "9007199254740993")
	st = f.compile(rpcReq(f, "best_mood", nil))
	if st.Mutation || st.Shape != compile.ShapeScalar {
		t.Errorf("stable scalar: %+v", st)
	}
}

func TestRPCTypedArguments(t *testing.T) {
	f := rpcFixture(t)
	st := f.compile(rpcReq(f, "home", map[string]any{
		"a":    `{"street":"1 Main","city":"X","mood":"ok"}`,
		"prev": []any{`{"street":"2 Main"}`, nil},
	}))
	want := `SELECT CASE WHEN (__fn.v)::text IS NULL THEN NULL ELSE jsonb_build_object('street', (__fn.v)."street", 'city', (__fn.v)."city", 'mood', (__fn.v)."mood") END AS data
FROM (SELECT "public"."home"(jsonb_populate_record(NULL::"public"."address", $1::jsonb), ARRAY(SELECT jsonb_populate_recordset(NULL::"public"."address", $2::jsonb))) AS v) AS __fn`
	wantSQL(t, st, want, `{"street":"1 Main","city":"X","mood":"ok"}`, `[{"street":"2 Main"},null]`)

	pt, err := postgis.Parse("POINT(0 1)", postgis.Geometry)
	if err != nil {
		t.Fatal(err)
	}
	st = f.compile(rpcReq(f, "near_me", map[string]any{"p": pt, "opts": `{"k":1}`, "moods": []any{"sad", "ok"}}))
	if !strings.Contains(st.SQL, `"public"."near_me"(ST_GeomFromEWKT($1), $2::"jsonb", $3::text[]::"public"."mood"[])`) {
		t.Errorf("typed args: %s", st.SQL)
	}
	if !strings.HasPrefix(st.SQL, "SELECT ST_AsGeoJSON(__fn.v)::jsonb AS data") {
		t.Errorf("spatial return: %s", st.SQL)
	}
	st = f.compile(rpcReq(f, "near_me", nil))
	if !strings.Contains(st.SQL, `(NULL::"public"."geometry", NULL::"jsonb", NULL::"public"."mood"[])`) {
		t.Errorf("null typed args: %s", st.SQL)
	}
}

func TestRPCVoidRowSetOf(t *testing.T) {
	f := rpcFixture(t)
	st := f.compile(rpcReq(f, "noop", nil))
	wantSQL(t, st, `SELECT 'null'::jsonb AS data FROM (SELECT "public"."noop"()) AS __v`)
	if st.Shape != compile.ShapeVoid || !st.Mutation {
		t.Errorf("void: %+v", st)
	}

	posts := f.res("posts")
	req := rpcReq(f, "publish_post", map[string]any{"post_id": int64(7)})
	req.Select = fields(posts, "id", "title")
	st = f.compile(req)
	wantSQL(t, st, "SELECT CASE WHEN t0 IS NULL THEN NULL ELSE jsonb_build_object('id', t0.\"id\", 'title', t0.\"title\") END AS data\nFROM \"public\".\"publish_post\"($1::\"int4\") AS t0", int64(7))
	if st.Shape != compile.ShapeScalar {
		t.Errorf("row shape %s", st.Shape)
	}

	req = rpcReq(f, "search_posts", map[string]any{"term": "x"})
	req.Method = plan.GET
	req.Select = fields(posts, "id")
	req.Page = plan.Page{Limit: 2, Offset: 2}
	req.Count = true
	req.Filter = cond(posts.Table.Column("author_id"), plan.OpEq, int64(1))
	st = f.compile(req)
	want := `SELECT jsonb_build_object('rows', coalesce(jsonb_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $5), '[]'::jsonb), 'has_next', coalesce(bool_or(s1.__rn > $5), false), 'has_prev', true, 'total', (SELECT count(*) FROM "public"."search_posts"($1::"text") c2 WHERE c2."author_id" = $6)) AS data
FROM (
  SELECT jsonb_build_object('id', t0."id") AS data, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."search_posts"($1::"text") t0
  WHERE t0."author_id" = $2
  ORDER BY t0."id" ASC
  LIMIT $3
  OFFSET $4
) s1`
	wantSQL(t, st, want, "x", int64(1), 3, 2, 4, int64(1))
	if st.Shape != compile.ShapePage || st.Cursors || !st.Total {
		t.Errorf("setof shape: %+v", st)
	}
	req.Page.After = []any{int64(1)}
	userErr(t, f.compileErr(req), compile.CodeKeysetUnsupported)

	// SETOF table embeds keep the plain table as their row source.
	req = rpcReq(f, "search_posts", map[string]any{"term": "x"})
	req.Select = &plan.Node{Resource: posts, Fields: []plan.FieldSel{{Column: posts.Table.Column("id")}},
		Embeds: []plan.Embed{{Key: "users", Relation: f.rel("posts", "users", ""), Node: fields(f.res("users"), "email")}}}
	st = f.compile(req)
	if !strings.Contains(st.SQL, `FROM "public"."users" t1`) || strings.Count(st.SQL, `"public"."search_posts"($1::"text")`) != 1 {
		t.Errorf("embed source: %s", st.SQL)
	}
}

func TestRPCSetOfValuesAndMint(t *testing.T) {
	f := rpcFixture(t)
	req := rpcReq(f, "mood_moods", nil)
	req.Page = plan.Page{Limit: 5, Offset: 1}
	st := f.compile(req)
	wantSQL(t, st, "SELECT coalesce(jsonb_agg(to_jsonb(__s.v)), '[]'::jsonb) AS data\nFROM (SELECT __v.v FROM \"public\".\"mood_moods\"() AS __v(v) LIMIT $1 OFFSET $2) AS __s", 5, 1)
	if st.Shape != compile.ShapeScalar {
		t.Errorf("setof scalar shape %s", st.Shape)
	}
	req.Page.Limit = 101
	userErr(t, f.compileErr(req), compile.CodePageSizeExceeded)

	st = f.compile(rpcReq(f, "addresses", nil))
	if !strings.Contains(st.SQL, `jsonb_agg(CASE WHEN (__s.v)::text IS NULL THEN NULL ELSE jsonb_build_object('street', (__s.v)."street"`) ||
		!strings.Contains(st.SQL, `FROM (SELECT __v AS v FROM "public"."addresses"() AS __v LIMIT $1) AS __s`) {
		t.Errorf("setof composite: %s", st.SQL)
	}

	st = f.compile(rpcReq(f, "authenticate", map[string]any{"user_email": "a@b"}))
	wantSQL(t, st, "SELECT to_jsonb(__fn.v) AS data\nFROM (SELECT \"public\".\"authenticate\"($1::\"text\") AS v) AS __fn", "a@b")
	if st.Shape != compile.ShapeJWT || !st.Mutation {
		t.Errorf("mint: %+v", st)
	}
	st = f.compile(rpcReq(f, "all_tokens", nil))
	wantSQL(t, st, "SELECT coalesce(jsonb_agg(to_jsonb(__s.v)), '[]'::jsonb) AS data\nFROM (SELECT __v AS v FROM \"public\".\"all_tokens\"() AS __v LIMIT $1) AS __s", 100)
	if st.Shape != compile.ShapeJWTSet {
		t.Errorf("mint set shape %s", st.Shape)
	}
}

func TestRPCCost(t *testing.T) {
	f := rpcFixture(t)
	req := rpcReq(f, "search_posts", nil)
	req.Method = plan.GET
	req.Page = plan.Page{Limit: 10}
	if c := compile.Cost(req, 100); c != 1+10*len(f.res("posts").Columns) {
		t.Errorf("setof cost = %d", c)
	}
	if c := compile.Cost(rpcReq(f, "best_mood", nil), 100); c != 1 {
		t.Errorf("scalar cost = %d", c)
	}
}
