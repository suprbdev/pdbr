package compile_test

import (
	"context"
	"testing"

	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/plan"
)

func BenchmarkCompile(b *testing.B) {
	fx := newFixture(b)
	users := fx.res("users")
	posts := fx.res("posts")
	cases := map[string]*plan.Request{
		"single_row": {Method: plan.GET, Resource: users, Key: &plan.Key{Constraint: users.Table.PrimaryKey, Values: []any{1}}, Select: fields(users, "id", "email")},
		"embeds": func() *plan.Request {
			p := list(users)
			p.Select = &plan.Node{Resource: users, Fields: []plan.FieldSel{{Column: fx.col("users", "id")}}, Embeds: []plan.Embed{{
				Key: "posts", Relation: fx.rel("users", "posts", ""),
				Node: &plan.Node{Resource: posts, Fields: []plan.FieldSel{{Column: fx.col("posts", "id")}}, Embeds: []plan.Embed{{
					Key: "comments", Relation: fx.rel("posts", "comments", ""), Node: fields(fx.res("comments"), "id"),
				}}},
			}}}
			return p
		}(),
		"list_filters": func() *plan.Request {
			p := list(users)
			p.Select = fields(users, "id", "email")
			p.Filter = plan.And{Terms: []plan.Expr{
				cond(fx.col("users", "mood"), plan.OpIn, []any{"happy", "ok"}),
				cond(fx.col("users", "email"), plan.OpILike, "*@example.com"),
				plan.Or{Terms: []plan.Expr{cond(fx.col("users", "tags"), plan.OpCs, []any{"admin"}), cond(fx.col("users", "settings"), plan.OpHk, "theme")}},
			}}
			p.Count = true
			return p
		}(),
		"keyset": func() *plan.Request {
			p := list(posts)
			p.Select = fields(posts, "id", "title")
			p.Order = []plan.OrderTerm{{Column: fx.col("posts", "title")}, {Column: fx.col("posts", "published"), Desc: true}}
			p.Page = plan.Page{Limit: 20, After: []any{7}}
			return p
		}(),
		"rpc": func() *plan.Request {
			p := rpcReq(fx, "search_posts", map[string]any{"term": "pg"})
			p.Select = fields(posts, "id", "title")
			return p
		}(),
	}
	for name, p := range cases {
		b.Run(name, func(b *testing.B) {
			req := &compile.Request{Plan: p, Built: fx.built}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := fx.c.Compile(context.Background(), req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
