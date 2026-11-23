package compile_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/suprbdev/pdbcore/contrib"
	"github.com/suprbdev/pdbcore/introspect"

	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/plan"
)

// FuzzFilter drives arbitrary values through a fixed filter shape and
// asserts the invariant that makes injection impossible: user values never
// appear in the SQL text, only in the parameter list — any two value sets
// produce byte-identical SQL and the same parameter count. The second
// statement covers the pdbcore/contrib operators and order terms over the
// golden-only kb.docs table, the last two an aggregated root (grouped scan,
// group count, casts) and an aggregated embed.
func FuzzFilter(f *testing.F) {
	fx := newFixture(f)
	cfx := &fixture{t: f, built: goldenBuilt(f)}
	cfx.c = compile.New(cfx.built)
	docs := cfx.res("kb.docs")
	dc := func(name string) *introspect.Column { return cfx.col("kb.docs", name) }
	users := fx.res("users")
	email := fx.col("users", "email")
	id := fx.col("users", "id")
	tags := fx.col("users", "tags")
	settings := fx.col("users", "settings")
	mood := fx.col("users", "mood")

	compileWith := func(t testing.TB, e, tag string, n int64) *compile.Statement {
		t.Helper()
		p := list(users)
		p.Select = &plan.Node{Resource: users, Fields: []plan.FieldSel{{Column: id}}, Embeds: []plan.Embed{{
			Key: "posts", Relation: fx.rel("users", "posts", ""), Node: fields(fx.res("posts"), "id"),
			Filter: cond(fx.col("posts", "title"), plan.OpILike, e),
		}}}
		p.Filter = plan.And{Terms: []plan.Expr{
			cond(email, plan.OpLike, e),
			cond(email, plan.OpSw, e),
			cond(email, plan.OpEw, tag),
			plan.Or{Terms: []plan.Expr{
				cond(id, plan.OpGt, n),
				cond(tags, plan.OpCs, []any{tag}),
				cond(mood, plan.OpIn, []any{e, tag}),
			}},
			cond(settings, plan.OpJpe, e),
			cond(settings, plan.OpJpm, tag),
			cond(settings, plan.OpHk, e),
			cond(settings, plan.OpCs, e),
			plan.Not{Term: cond(email, plan.OpEq, tag)},
		}}
		p.Page = plan.Page{Limit: 10, After: []any{n}}
		p.Order = []plan.OrderTerm{{Column: fx.col("users", "full_name"), Desc: true}}
		p.Count = true
		stmt, err := fx.c.Compile(context.Background(), &compile.Request{Plan: p, Built: fx.built})
		if err != nil {
			t.Fatalf("compile error on valid input: %v", err)
		}
		return stmt
	}
	compileContrib := func(t testing.TB, e, tag string, n int64) *compile.Statement {
		t.Helper()
		fts := contrib.FullTextQuery{Query: e, Config: "c" + tag}
		ftsFilter := cond(dc("search"), plan.OpWFts, fts)
		rng := contrib.RangeValue{Start: &e, End: &tag, StartInclusive: true}
		p := list(docs)
		p.Select = fields(docs, "id")
		p.Filter = plan.And{Terms: []plan.Expr{
			ftsFilter,
			cond(dc("body"), plan.OpFts, contrib.FullTextQuery{Query: tag}),
			cond(dc("title"), "trgm_sim", e),
			cond(dc("title"), "trgm_swsim", tag),
			cond(dc("email"), plan.OpEq, e),
			cond(dc("email"), plan.OpILike, tag),
			cond(dc("email"), plan.OpSw, e),
			cond(dc("email"), plan.OpIn, []any{e, tag}),
			cond(dc("email"), plan.OpIsDistinct, tag),
			cond(dc("attrs"), plan.OpHk, e),
			cond(dc("attrs"), "hk_all", []string{e, tag}),
			cond(dc("attrs"), plan.OpCs, map[string]any{"k": e, "v": tag}),
			cond(dc("attrs"), plan.OpCd, e),
			cond(dc("path"), "ancestor", e),
			cond(dc("path"), "lquery_any", []string{e, tag}),
			cond(dc("paths"), "ltxtquery", tag),
			cond(dc("valid"), plan.OpOv, rng),
			cond(dc("valid"), plan.OpCs, "x"+tag),
			cond(dc("valid"), plan.OpAdj, rng),
			cond(dc("slots"), plan.OpSl, contrib.RangeValue{Start: new(fmt.Sprint(n)), End: new(fmt.Sprint(n + 1))}),
			cond(dc("pages"), plan.OpCs, n),
			cond(dc("net"), "cd_strict", e),
			plan.Not{Term: cond(dc("net"), plan.OpOv, tag)},
		}}
		p.Order = []plan.OrderTerm{
			{Contrib: &plan.ContribOrder{Column: dc("search"), Name: "rank", Rank: "ts_rank", Filter: &ftsFilter}, Desc: true},
			{Contrib: &plan.ContribOrder{Column: dc("title"), Name: "trgm_dist", Expr: "distance"}},
			{Column: dc("email")},
		}
		p.Trgm = e
		p.Page = plan.Page{Limit: 10, After: []any{n}}
		stmt, err := cfx.c.Compile(context.Background(), &compile.Request{Plan: p, Built: cfx.built})
		if err != nil {
			t.Fatalf("contrib compile error on valid input: %v", err)
		}
		return stmt
	}
	compileAgg := func(t testing.TB, e, tag string, n int64) *compile.Statement {
		t.Helper()
		pg := func(name string) *plan.TypeRef { return &plan.TypeRef{Schema: "pg_catalog", Name: name} }
		p := list(users)
		p.Select = &plan.Node{Resource: users, Fields: []plan.FieldSel{
			{Column: mood},
			{Computed: fx.computed("users", "post_count")},
			{Agg: &plan.Aggregate{Func: plan.AggCount}},
			{Alias: "total", Column: fx.col("users", "balance"), Agg: &plan.Aggregate{Func: plan.AggSum, OutputCast: pg("text")}},
			{Column: id, Agg: &plan.Aggregate{Func: plan.AggAvg, InputCast: pg("float8")}},
		}}
		p.Filter = plan.And{Terms: []plan.Expr{
			cond(email, plan.OpSw, e),
			plan.Or{Terms: []plan.Expr{cond(id, plan.OpGt, n), cond(mood, plan.OpIn, []any{e, tag})}},
		}}
		p.Order = []plan.OrderTerm{{Column: mood, Desc: true}}
		p.Page = plan.Page{Limit: 10, Offset: 2}
		p.Count = true
		stmt, err := fx.c.Compile(context.Background(), &compile.Request{Plan: p, Built: fx.built})
		if err != nil {
			t.Fatalf("aggregate compile error on valid input: %v", err)
		}
		return stmt
	}
	compileAggEmbed := func(t testing.TB, e, tag string, n int64) *compile.Statement {
		t.Helper()
		posts := fx.res("posts")
		p := list(users)
		p.Select = &plan.Node{Resource: users, Fields: []plan.FieldSel{{Column: id}}, Embeds: []plan.Embed{{
			Key: "posts", Relation: fx.rel("users", "posts", ""), Page: plan.Page{Limit: 3, Offset: 1},
			Node: &plan.Node{Resource: posts, Fields: []plan.FieldSel{
				{Column: fx.col("posts", "published")},
				{Column: fx.col("posts", "id"), Agg: &plan.Aggregate{Func: plan.AggMax}},
			}},
			Filter: plan.Or{Terms: []plan.Expr{cond(fx.col("posts", "title"), plan.OpILike, e), cond(fx.col("posts", "id"), plan.OpLt, n)}},
			Order:  []plan.OrderTerm{{Column: fx.col("posts", "published"), Desc: true}},
		}}}
		p.Filter = cond(email, plan.OpEw, tag)
		stmt, err := fx.c.Compile(context.Background(), &compile.Request{Plan: p, Built: fx.built})
		if err != nil {
			t.Fatalf("aggregate embed compile error on valid input: %v", err)
		}
		return stmt
	}
	baseline := compileWith(f, "a@example.com", "admin", 5)
	contribBaseline := compileContrib(f, "a@example.com", "admin", 5)
	aggBaseline := compileAgg(f, "a@example.com", "admin", 5)
	aggEmbedBaseline := compileAggEmbed(f, "a@example.com", "admin", 5)

	f.Add("a@example.com", "admin", int64(5))
	f.Add(`'; DROP TABLE users; --`, `"]}`, int64(-1))
	f.Add("%_\\*", "'", int64(0))
	f.Add("", "", int64(1)<<40)
	f.Add("$1 || '", "\x00", int64(7))

	f.Fuzz(func(t *testing.T, e, tag string, n int64) {
		for _, c := range []struct{ stmt, baseline *compile.Statement }{
			{compileWith(t, e, tag, n), baseline},
			{compileContrib(t, e, tag, n), contribBaseline},
			{compileAgg(t, e, tag, n), aggBaseline},
			{compileAggEmbed(t, e, tag, n), aggEmbedBaseline},
		} {
			if c.stmt.SQL != c.baseline.SQL {
				t.Fatalf("SQL text varied with input values:\n--- baseline ---\n%s\n--- got ---\n%s", c.baseline.SQL, c.stmt.SQL)
			}
			if len(c.stmt.Args) != len(c.baseline.Args) {
				t.Fatalf("parameter count varied: %d != %d", len(c.stmt.Args), len(c.baseline.Args))
			}
		}
	})
}

// FuzzSpatial is FuzzFilter for the PostGIS path: WKT strings, bounding-box
// coordinates and distances are user data and must move through the
// parameter list exactly like scalars. Bare WKT and EWKT are distinct input
// encodings (bare WKT is tagged with the column's SRID), so each has its own
// baseline.
func FuzzSpatial(f *testing.F) {
	fx := newFixture(f)
	places := fx.res("places")
	loc := fx.col("places", "location")

	compileWith := func(t testing.TB, wkt string, x, d float64) (*compile.Statement, bool) {
		t.Helper()
		p := list(places)
		p.Select = fields(places, "id")
		p.Filter = plan.And{Terms: []plan.Expr{
			cond(loc, "st_intersects", wkt),
			cond(loc, "bbox_intersects", []any{x, x, x, x}),
			cond(loc, "st_dwithin", []any{wkt, d}),
			cond(loc, "bbox_leftof", wkt),
			cond(loc, "st_exactlyequals", wkt),
			cond(loc, "bbox_intersectsnd", wkt),
		}}
		p.Order = []plan.OrderTerm{{Distance: loc}}
		p.Near = wkt
		stmt, err := fx.c.Compile(context.Background(), &compile.Request{Plan: p, Built: fx.built})
		if err != nil {
			// Malformed geometry is rejected at compile time by design; only
			// a successful compile is subject to the invariant.
			return nil, false
		}
		return stmt, true
	}
	baseline, ok := compileWith(f, "POINT(1 2)", 1.5, 100)
	if !ok {
		f.Fatal("baseline spatial query failed to compile")
	}
	ewktBaseline, ok := compileWith(f, "SRID=4326;POINT(1 2)", 1.5, 100)
	if !ok {
		f.Fatal("EWKT baseline spatial query failed to compile")
	}
	f.Add("POINT(1 2)", 1.5, 100.0)
	f.Add("SRID=4326;POLYGON((0 0,1 0,1 1,0 0))", -0.5, 0.0)
	f.Add(`POINT(1 2)'); DROP TABLE places; --`, 0.0, 1.0)
	f.Add(`'; DROP TABLE places; --`, 1e30, -1.0)
	f.Add("", 0.0, 0.0)

	f.Fuzz(func(t *testing.T, wkt string, x, d float64) {
		stmt, ok := compileWith(t, wkt, x, d)
		if !ok {
			return
		}
		if stmt.SQL != baseline.SQL && stmt.SQL != ewktBaseline.SQL {
			t.Fatalf("spatial SQL text varied with input values:\n--- baseline ---\n%s\n--- got ---\n%s", baseline.SQL, stmt.SQL)
		}
		if len(stmt.Args) != len(baseline.Args) {
			t.Fatalf("parameter count varied: %d != %d", len(stmt.Args), len(baseline.Args))
		}
	})
}
