package compile_test

import (
	"context"
	"testing"

	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/plan"
)

// FuzzFilter drives arbitrary values through a fixed filter shape and
// asserts the invariant that makes injection impossible: user values never
// appear in the SQL text, only in the parameter list — any two value sets
// produce byte-identical SQL and the same parameter count.
func FuzzFilter(f *testing.F) {
	fx := newFixture(f)
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
	baseline := compileWith(f, "a@example.com", "admin", 5)

	f.Add("a@example.com", "admin", int64(5))
	f.Add(`'; DROP TABLE users; --`, `"]}`, int64(-1))
	f.Add("%_\\*", "'", int64(0))
	f.Add("", "", int64(1)<<40)
	f.Add("$1 || '", "\x00", int64(7))

	f.Fuzz(func(t *testing.T, e, tag string, n int64) {
		stmt := compileWith(t, e, tag, n)
		if stmt.SQL != baseline.SQL {
			t.Fatalf("SQL text varied with input values:\n--- baseline ---\n%s\n--- got ---\n%s", baseline.SQL, stmt.SQL)
		}
		if len(stmt.Args) != len(baseline.Args) {
			t.Fatalf("parameter count varied: %d != %d", len(stmt.Args), len(baseline.Args))
		}
	})
}

// FuzzSpatial is FuzzFilter for the PostGIS path: WKT strings, bounding-box
// coordinates and distances are user data and must move through the
// parameter list exactly like scalars.
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
		if stmt.SQL != baseline.SQL {
			t.Fatalf("spatial SQL text varied with input values:\n--- baseline ---\n%s\n--- got ---\n%s", baseline.SQL, stmt.SQL)
		}
		if len(stmt.Args) != len(baseline.Args) {
			t.Fatalf("parameter count varied: %d != %d", len(stmt.Args), len(baseline.Args))
		}
	})
}
