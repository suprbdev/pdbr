package compile_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
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
// golden-only kb.docs table, the third pgvector distance ordering, output
// and input over kb.embeddings, the next cube and earthdistance filters,
// orderings, keyset cursors, scores, output and input over kb.shapes and
// kb.sites, the next two an aggregated root (grouped scan,
// group count, casts) and an aggregated embed, the last two JSON paths
// (keys, indexes and the path-derived response keys are values) in select,
// filters, order, casts and grouping, then spread embeds (to-one, to-many,
// spread grouping), then select= aliases on columns, paths, computed
// columns, embeds, spread items and aggregates (response keys the request
// supplies are values too); a path-derived key equal to another item's key
// is a duplicate-key error, which the path and spread-grouping statements
// skip.
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
		bodyFilter := cond(dc("body"), plan.OpFts, contrib.FullTextQuery{Query: tag})
		p.Select.Fields = append(p.Select.Fields, plan.FieldSel{Pseudo: docs.Pseudo["title_similarity"]}, plan.FieldSel{Pseudo: docs.Pseudo["title_strict_word_similarity"]},
			plan.FieldSel{Pseudo: docs.Pseudo["search_headline"], Headline: &plan.Headline{Document: dc("title"), Options: `StartSel="` + e + `"`, Filter: &ftsFilter}},
			plan.FieldSel{Alias: "b", Pseudo: docs.Pseudo["body_headline"], Headline: &plan.Headline{Document: dc("body"), Options: tag, Filter: &bodyFilter}})
		p.Filter = plan.And{Terms: []plan.Expr{
			ftsFilter,
			bodyFilter,
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
			cond(dc("body"), "unaccent_eq", e),
			cond(dc("body"), "unaccent_ilike", tag),
			plan.Or{Terms: []plan.Expr{cond(dc("body"), "unaccent_sw", e), plan.Not{Term: cond(dc("title"), "unaccent_includes", tag)}}},
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
	compilePath := func(t testing.TB, e, tag string, n int64) *compile.Statement {
		t.Helper()
		idx := int(int32(n))
		path := func(text bool) plan.JSONPath {
			return plan.JSONPath{{Key: e}, {Array: true, Index: idx}, {Key: tag, Text: text}}
		}
		pc := func(p plan.JSONPath, op plan.Op, v any) plan.Cond {
			c := cond(settings, op, v)
			c.Path = p
			return c
		}
		pg := func(name string) *plan.TypeRef { return &plan.TypeRef{Schema: "pg_catalog", Name: name} }
		p := list(users)
		p.Select = &plan.Node{Resource: users, Fields: []plan.FieldSel{
			{Column: settings, Path: path(true)},
			{Alias: "c", Column: settings, Path: path(false), Cast: pg("int4")},
			{Alias: "a", Column: tags, Path: plan.JSONPath{{Array: true, Index: idx, Text: true}}},
			{Column: id, Cast: pg("text")},
		}}
		p.Filter = plan.And{Terms: []plan.Expr{
			pc(path(true), plan.OpEq, e),
			pc(path(true), plan.OpIn, []any{e, tag}),
			pc(path(false), plan.OpCs, tag),
			pc(path(false), plan.OpGt, tag),
			plan.Not{Term: pc(path(true), plan.OpLike, e)},
			modCond(pc(path(true), plan.OpMatch, []any{e, tag}), plan.ModAny),
			cond(email, plan.OpMatch, e),
			plan.Not{Term: cond(email, plan.OpIMatch, tag)},
			modCond(cond(email, plan.OpLike, []any{e, tag}), plan.ModAll),
			modCond(cond(email, plan.OpILike, []any{tag}), plan.ModAny),
			modCond(cond(id, plan.OpGt, []any{n, n}), plan.ModAll),
			modCond(cond(mood, plan.OpEq, []any{e}), plan.ModAny),
		}}
		p.Order = []plan.OrderTerm{{Column: settings, Path: path(true), Desc: true}, {Column: settings, Path: path(false)}}
		p.Page = plan.Page{Limit: 10, After: []any{n}}
		stmt, err := fx.c.Compile(context.Background(), &compile.Request{Plan: p, Built: fx.built})
		if duplicateKey(err) {
			return nil
		}
		if err != nil {
			t.Fatalf("path compile error on valid input: %v", err)
		}
		return stmt
	}
	compilePathAgg := func(t testing.TB, e, tag string, n int64) *compile.Statement {
		t.Helper()
		path := plan.JSONPath{{Key: e}, {Key: tag, Text: true}}
		p := list(users)
		p.Select = &plan.Node{Resource: users, Fields: []plan.FieldSel{
			{Column: settings, Path: path},
			{Agg: &plan.Aggregate{Func: plan.AggCount}},
			{Alias: "s", Column: settings, Path: plan.JSONPath{{Key: tag}}, Agg: &plan.Aggregate{Func: plan.AggSum, InputCast: &plan.TypeRef{Schema: "pg_catalog", Name: "numeric"}}},
		}}
		p.Order = []plan.OrderTerm{{Column: settings, Path: path, Desc: true}}
		p.Page = plan.Page{Limit: 10, Offset: 2}
		p.Count = true
		stmt, err := fx.c.Compile(context.Background(), &compile.Request{Plan: p, Built: fx.built})
		if duplicateKey(err) {
			return nil
		}
		if err != nil {
			t.Fatalf("path aggregate compile error on valid input: %v", err)
		}
		return stmt
	}
	compileSpread := func(t testing.TB, e, tag string, n int64) *compile.Statement {
		t.Helper()
		posts := fx.res("posts")
		title := fx.col("posts", "title")
		p := list(users)
		p.Select = &plan.Node{Resource: users, Fields: []plan.FieldSel{{Column: id}}, Embeds: []plan.Embed{
			{Key: "posts", Spread: true, Relation: fx.rel("users", "posts", ""), Page: plan.Page{Limit: 3},
				Node:   &plan.Node{Resource: posts, Fields: []plan.FieldSel{{Alias: "t", Column: title}, {Column: fx.col("posts", "id"), Alias: "i"}}},
				Filter: cond(title, plan.OpILike, e), Order: []plan.OrderTerm{{Column: title, Desc: true}}},
			{Key: "places", Spread: true, Inner: true, Relation: fx.rel("users", "places", ""),
				Node: &plan.Node{Resource: fx.res("places"), Fields: []plan.FieldSel{{Alias: "pn", Column: fx.col("places", "name")}}}, Filter: cond(fx.col("places", "name"), plan.OpEq, tag)},
		}}
		p.Filter = cond(email, plan.OpEq, e)
		p.Page = plan.Page{Limit: 10, After: []any{n}}
		stmt, err := fx.c.Compile(context.Background(), &compile.Request{Plan: p, Built: fx.built})
		if err != nil {
			t.Fatalf("spread compile error on valid input: %v", err)
		}
		return stmt
	}
	compileSpreadAgg := func(t testing.TB, e, tag string, n int64) *compile.Statement {
		t.Helper()
		posts := fx.res("posts")
		p := list(posts)
		p.Select = &plan.Node{Resource: posts, Fields: []plan.FieldSel{{Agg: &plan.Aggregate{Func: plan.AggCount}}}, Embeds: []plan.Embed{{
			Key: "users", Spread: true, Relation: fx.rel("posts", "users", ""),
			Node: &plan.Node{Resource: users, Fields: []plan.FieldSel{
				{Column: mood},
				{Alias: "m", Column: id, Agg: &plan.Aggregate{Func: plan.AggMax}},
				{Column: settings, Path: plan.JSONPath{{Key: e, Text: true}}},
			}},
			Filter: cond(email, plan.OpSw, tag),
		}}}
		p.Filter = cond(fx.col("posts", "id"), plan.OpGt, n)
		p.Page = plan.Page{Limit: 10, Offset: 2}
		p.Count = true
		stmt, err := fx.c.Compile(context.Background(), &compile.Request{Plan: p, Built: fx.built})
		if duplicateKey(err) {
			return nil
		}
		if err != nil {
			t.Fatalf("spread aggregate compile error on valid input: %v", err)
		}
		return stmt
	}
	compileAlias := func(t testing.TB, e, tag string, n int64) *compile.Statement {
		t.Helper()
		posts := fx.res("posts")
		p := list(users)
		p.Select = &plan.Node{Resource: users, Fields: []plan.FieldSel{
			{Alias: "a" + e, Column: id},
			{Alias: "b" + tag, Column: settings, Path: plan.JSONPath{{Key: e}}},
			{Alias: "c" + e, Computed: fx.computed("users", "post_count")},
		}, Embeds: []plan.Embed{
			{Key: "posts", Alias: "d" + tag, Relation: fx.rel("users", "posts", ""), Node: &plan.Node{Resource: posts, Fields: []plan.FieldSel{{Alias: "e" + e, Column: fx.col("posts", "title")}}}},
			{Key: "places", Spread: true, Relation: fx.rel("users", "places", ""), Node: &plan.Node{Resource: fx.res("places"), Fields: []plan.FieldSel{{Alias: "f" + tag, Column: fx.col("places", "name")}}}},
		}}
		p.Filter = cond(id, plan.OpGt, n)
		stmt, err := fx.c.Compile(context.Background(), &compile.Request{Plan: p, Built: fx.built})
		if err != nil {
			t.Fatalf("alias compile error on valid input: %v", err)
		}
		return stmt
	}
	compileAliasAgg := func(t testing.TB, e, tag string, n int64) *compile.Statement {
		t.Helper()
		p := list(users)
		p.Select = &plan.Node{Resource: users, Fields: []plan.FieldSel{
			{Alias: "a" + e, Column: mood},
			{Alias: "b" + tag, Agg: &plan.Aggregate{Func: plan.AggCount}},
			{Alias: "c" + e, Column: id, Agg: &plan.Aggregate{Func: plan.AggMax}},
		}}
		p.Filter = cond(id, plan.OpGt, n)
		p.Page = plan.Page{Limit: 10}
		stmt, err := fx.c.Compile(context.Background(), &compile.Request{Plan: p, Built: fx.built})
		if err != nil {
			t.Fatalf("alias aggregate compile error on valid input: %v", err)
		}
		return stmt
	}
	compileRowArgs := func(t testing.TB, e, tag string, n int64) *compile.Statement {
		t.Helper()
		row := map[string]any{"email": e, "full_name": tag, "id": n, "tags": []any{e, tag}}
		var st *compile.Statement
		for _, req := range []*plan.Request{
			rpcReq(fx, "user_greeting", map[string]any{"greeting": e, "u": row}),
			rpcReq(fx, "total_title_length", map[string]any{"ps": []any{row, nil, map[string]any{"title": tag}}}),
		} {
			stmt, err := fx.c.Compile(context.Background(), &compile.Request{Plan: req, Built: fx.built})
			if err != nil {
				t.Fatalf("row argument compile error on valid input: %v", err)
			}
			if st == nil {
				st = stmt
			} else {
				st.SQL += "\n" + stmt.SQL
				st.Args = append(st.Args, stmt.Args...)
			}
		}
		return st
	}
	baseline := compileWith(f, "a@example.com", "admin", 5)
	rowArgsBaseline := compileRowArgs(f, "a@example.com", "admin", 5)
	aliasBaseline := compileAlias(f, "a@example.com", "admin", 5)
	aliasAggBaseline := compileAliasAgg(f, "a@example.com", "admin", 5)
	spreadBaseline := compileSpread(f, "a@example.com", "admin", 5)
	spreadAggBaseline := compileSpreadAgg(f, "a@example.com", "admin", 5)
	pathBaseline := compilePath(f, "a@example.com", "admin", 5)
	pathAggBaseline := compilePathAgg(f, "a@example.com", "admin", 5)
	contribBaseline := compileContrib(f, "a@example.com", "admin", 5)
	compileContribDerived := func(t testing.TB, e, tag string, n int64) *compile.Statement {
		t.Helper()
		clip := func(s string) string {
			if len(s) > 60 {
				return s[:60]
			}
			return s
		}
		off := n % 1000
		length := n % 7
		if length < 0 {
			length = -length
		}
		hp := func(key string, text bool) plan.JSONPath { return plan.JSONPath{{Key: key, Text: text}} }
		p := list(docs)
		p.Select = fields(docs, "id")
		p.Select.Fields = append(p.Select.Fields,
			plan.FieldSel{Column: dc("attrs"), Path: hp(e, false)},
			plan.FieldSel{Alias: "v", Column: dc("attrs"), Path: hp(tag, true)},
			plan.FieldSel{Pseudo: docs.Pseudo["path_nlevel"]},
			plan.FieldSel{Pseudo: docs.Pseudo["path_parent"]},
			plan.FieldSel{Pseudo: docs.Pseudo["paths_lca"]},
			plan.FieldSel{Pseudo: docs.Pseudo["path_lca"], Args: map[string]any{"path": e}},
			plan.FieldSel{Pseudo: docs.Pseudo["path_subpath"], Args: map[string]any{"offset": off, "length": length}},
			plan.FieldSel{Alias: "rest", Pseudo: docs.Pseudo["path_subpath"], Args: map[string]any{"offset": off}},
			plan.FieldSel{Pseudo: docs.Pseudo["body_levenshtein_distance"]},
		)
		p.Filter = plan.And{Terms: []plan.Expr{
			plan.Cond{Column: dc("attrs"), Path: hp(e, true), Op: plan.OpEq, Value: tag},
			plan.Cond{Column: dc("attrs"), Path: hp(tag, false), Op: plan.OpLike, Value: e},
			cond(dc("attrs"), "hk_def", e),
			plan.Cond{Pseudo: docs.Pseudo["path_nlevel"], Op: plan.OpGte, Value: n},
			plan.Not{Term: plan.Cond{Pseudo: docs.Pseudo["path_nlevel"], Op: plan.OpIn, Value: []any{n, n + 1}}},
			cond(dc("body"), "levenshtein_lt", n&0xffff),
			cond(dc("body"), "soundex", e),
			plan.Or{Terms: []plan.Expr{cond(dc("body"), "metaphone", clip(tag)), plan.Not{Term: cond(dc("body"), "dmetaphone", e)}}},
		}}
		p.Order = []plan.OrderTerm{
			{Column: dc("attrs"), Path: hp(tag, false), Desc: true},
			{Pseudo: docs.Pseudo["path_nlevel"]},
			{Contrib: &plan.ContribOrder{Column: dc("body"), Name: "levenshtein", Expr: "levenshteinDistance"}},
		}
		p.Levenshtein = clip(e)
		p.Page = plan.Page{Limit: 10, After: []any{n}}
		read, err := cfx.c.Compile(context.Background(), &compile.Request{Plan: p, Built: cfx.built})
		if duplicateKey(err) {
			return nil
		}
		if err != nil {
			t.Fatalf("contrib derived compile error on valid input: %v", err)
		}
		labels := cfx.res("kb.labels")
		q := list(labels)
		q.Select = fields(labels, "id")
		q.Select.Fields = append(q.Select.Fields, plan.FieldSel{Pseudo: labels.Pseudo["tags_icount"]})
		q.Filter = plan.Or{Terms: []plan.Expr{
			cond(cfx.col("kb.labels", "tags"), "query_int", e),
			plan.Cond{Pseudo: labels.Pseudo["tags_icount"], Op: plan.OpLt, Value: n},
		}}
		q.Order = []plan.OrderTerm{{Pseudo: labels.Pseudo["tags_icount"], Desc: true}}
		ia, err := cfx.c.Compile(context.Background(), &compile.Request{Plan: q, Built: cfx.built})
		if err != nil {
			t.Fatalf("intarray compile error on valid input: %v", err)
		}
		return &compile.Statement{SQL: read.SQL + "\n" + ia.SQL, Args: append(read.Args, ia.Args...)}
	}
	derivedBaseline := compileContribDerived(f, "a@example.com", "admin", 5)
	compileVector := func(t testing.TB, e, tag string, n int64) *compile.Statement {
		t.Helper()
		emb := cfx.res("kb.embeddings")
		ec := func(name string) *introspect.Column { return cfx.col("kb.embeddings", name) }
		vo := func(col, name, expr string) plan.OrderTerm {
			return plan.OrderTerm{Contrib: &plan.ContribOrder{Column: ec(col), Name: name, Expr: expr}}
		}
		p := list(emb)
		p.Select = fields(emb, "id", "emb", "half", "sparse")
		p.Select.Fields = append(p.Select.Fields, plan.FieldSel{Pseudo: emb.Pseudo["emb_cosine_distance"]}, plan.FieldSel{Pseudo: emb.Pseudo["sparse_l1_distance"]})
		p.Filter = plan.And{Terms: []plan.Expr{
			cond(ec("title"), plan.OpEq, e), plan.Not{Term: cond(ec("emb"), plan.OpIs, nil)},
			cond(ec("emb"), "vec_cos_lt", float64(n)/3),
			plan.Not{Term: cond(ec("emb"), "vec_ip_lt", float64(len(tag)))},
		}}
		p.Order = []plan.OrderTerm{vo("emb", "vec_l2", "l2Distance"), vo("half", "vec_cos", "cosineDistance"), vo("sparse", "vec_ip", "negativeInnerProduct")}
		p.Order[1].Desc = true
		p.Vec = []any{float64(n), 1.5, float64(len(tag))}
		p.Page = plan.Page{Limit: 10, After: []any{n}}
		read, err := cfx.c.Compile(context.Background(), &compile.Request{Plan: p, Built: cfx.built})
		if err != nil {
			t.Fatalf("vector compile error on valid input: %v", err)
		}
		w := &plan.Request{Method: plan.POST, Resource: emb, Select: fields(emb, "id", "emb"),
			Body: body(emb, map[string]any{"title": e, "emb": []any{float64(n), 2.0}, "half": "[" + tag + "]", "sparse": nil})}
		write, err := cfx.c.Compile(context.Background(), &compile.Request{Plan: w, Built: cfx.built})
		if err != nil {
			t.Fatalf("vector insert compile error on valid input: %v", err)
		}
		return &compile.Statement{SQL: read.SQL + "\n" + write.SQL, Args: append(read.Args, write.Args...)}
	}
	vectorBaseline := compileVector(f, "a@example.com", "admin", 5)
	compileCube := func(t testing.TB, e, tag string, n int64) *compile.Statement {
		t.Helper()
		shapes, sites := cfx.res("kb.shapes"), cfx.res("kb.sites")
		sc := func(name string) *introspect.Column { return cfx.col("kb.shapes", name) }
		st := func(name string) *introspect.Column { return cfx.col("kb.sites", name) }
		co := func(col *introspect.Column, name, expr string) plan.OrderTerm {
			return plan.OrderTerm{Contrib: &plan.ContribOrder{Column: col, Name: name, Expr: expr}}
		}
		p := list(shapes)
		p.Select = fields(shapes, "id", "box")
		p.Select.Fields = append(p.Select.Fields, plan.FieldSel{Pseudo: shapes.Pseudo["box_chebyshev_distance"]}, plan.FieldSel{Pseudo: shapes.Pseudo["pt_l1_distance"]})
		p.Filter = plan.And{Terms: []plan.Expr{
			cond(sc("box"), plan.OpCs, []any{float64(n), 1.5}),
			cond(sc("box"), plan.OpCd, "("+e+")"),
			plan.Not{Term: cond(sc("box"), plan.OpOv, []any{[]any{float64(n)}, []any{float64(len(tag))}})},
			cond(sc("box"), "vec_l2_lt", float64(n)/7),
			plan.Or{Terms: []plan.Expr{cond(sc("box"), "vec_chebyshev_lt", float64(len(e))), cond(sc("name"), plan.OpEq, tag)}},
		}}
		p.Order = []plan.OrderTerm{co(sc("box"), "vec_l1", "l1Distance"), co(sc("box"), "vec_chebyshev", "chebyshevDistance")}
		p.Order[1].Desc = true
		p.Vec = []any{float64(n), float64(len(tag))}
		p.Page = plan.Page{Limit: 10, After: []any{n}}
		cubes, err := cfx.c.Compile(context.Background(), &compile.Request{Plan: p, Built: cfx.built})
		if err != nil {
			t.Fatalf("cube compile error on valid input: %v", err)
		}
		q := list(sites)
		q.Select = fields(sites, "id", "loc", "ll")
		q.Select.Fields = append(q.Select.Fields, plan.FieldSel{Pseudo: sites.Pseudo["loc_earth_distance"]}, plan.FieldSel{Pseudo: sites.Pseudo["ll_earth_distance_miles"]})
		q.Filter = plan.And{Terms: []plan.Expr{
			cond(st("loc"), "earth_lt", float64(n)),
			plan.Not{Term: cond(st("ll"), "earth_miles_lt", float64(len(e)))},
			cond(st("name"), plan.OpSw, e),
		}}
		q.Order = []plan.OrderTerm{co(st("loc"), "earth_dist", "earthDistance"), co(st("ll"), "earth_miles", "earthDistanceMiles")}
		q.LatLon = map[string]any{"lat": float64(n % 90), "lon": float64(len(tag) % 180)}
		q.Page = plan.Page{Limit: 10, After: []any{n}}
		earth, err := cfx.c.Compile(context.Background(), &compile.Request{Plan: q, Built: cfx.built})
		if err != nil {
			t.Fatalf("earth compile error on valid input: %v", err)
		}
		ws := &plan.Request{Method: plan.POST, Resource: shapes, Select: fields(shapes, "id", "box"),
			Body: body(shapes, map[string]any{"name": e, "box": []any{[]any{float64(n), 0.0}, []any{1.0, float64(len(tag))}}, "pt": "(" + tag + ")"})}
		wshape, err := cfx.c.Compile(context.Background(), &compile.Request{Plan: ws, Built: cfx.built})
		if err != nil {
			t.Fatalf("cube insert compile error on valid input: %v", err)
		}
		we := &plan.Request{Method: plan.POST, Resource: sites, Select: fields(sites, "id", "loc"),
			Body: body(sites, map[string]any{"name": tag, "loc": map[string]any{"lat": float64(n % 90), "lon": -float64(len(e) % 180)}, "ll": e})}
		wsite, err := cfx.c.Compile(context.Background(), &compile.Request{Plan: we, Built: cfx.built})
		if err != nil {
			t.Fatalf("earth insert compile error on valid input: %v", err)
		}
		return &compile.Statement{
			SQL:  strings.Join([]string{cubes.SQL, earth.SQL, wshape.SQL, wsite.SQL}, "\n"),
			Args: append(append(append(cubes.Args, earth.Args...), wshape.Args...), wsite.Args...),
		}
	}
	cubeBaseline := compileCube(f, "a@example.com", "admin", 5)
	aggBaseline := compileAgg(f, "a@example.com", "admin", 5)
	aggEmbedBaseline := compileAggEmbed(f, "a@example.com", "admin", 5)

	f.Add("a@example.com", "admin", int64(5))
	f.Add(`'; DROP TABLE users; --`, `"]}`, int64(-1))
	f.Add("%_\\*", "'", int64(0))
	f.Add("", "", int64(1)<<40)
	f.Add("$1 || '", "\x00", int64(7))
	f.Add(`a'b\`, `\' || pg_sleep(1) --`, int64(3))

	f.Fuzz(func(t *testing.T, e, tag string, n int64) {
		for _, c := range []struct{ stmt, baseline *compile.Statement }{
			{compileWith(t, e, tag, n), baseline},
			{compileContrib(t, e, tag, n), contribBaseline},
			{compileContribDerived(t, e, tag, n), derivedBaseline},
			{compileVector(t, e, tag, n), vectorBaseline},
			{compileCube(t, e, tag, n), cubeBaseline},
			{compileAgg(t, e, tag, n), aggBaseline},
			{compileAggEmbed(t, e, tag, n), aggEmbedBaseline},
			{compilePath(t, e, tag, n), pathBaseline},
			{compilePathAgg(t, e, tag, n), pathAggBaseline},
			{compileSpread(t, e, tag, n), spreadBaseline},
			{compileSpreadAgg(t, e, tag, n), spreadAggBaseline},
			{compileAlias(t, e, tag, n), aliasBaseline},
			{compileAliasAgg(t, e, tag, n), aliasAggBaseline},
			{compileRowArgs(t, e, tag, n), rowArgsBaseline},
		} {
			if c.stmt == nil {
				continue
			}
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
// parameter list exactly like scalars, and so are the raster filter's
// geometry, the raster value's point and the bands of the raster
// pseudo-columns. Bare WKT and EWKT are distinct input
// encodings (bare WKT is tagged with the column's SRID), so each has its own
// baseline.
func FuzzSpatial(f *testing.F) {
	fx := &fixture{t: f, built: goldenBuilt(f)}
	fx.c = compile.New(fx.built)
	places := fx.res("places")
	loc := fx.col("places", "location")
	tiles := fx.res("kb.tiles")
	rast := tiles.Table.Column("rast")

	compileWith := func(t testing.TB, wkt string, x, d float64) (*compile.Statement, bool) {
		t.Helper()
		p := list(places)
		p.Select = fields(places, "id")
		digits := int64(math.Abs(math.Mod(x, 16)))
		p.Select.Fields = append(p.Select.Fields,
			plan.FieldSel{Pseudo: places.Pseudo["location_distance"], Args: map[string]any{"spheroid": true}},
			plan.FieldSel{Pseudo: places.Pseudo["area_distance"]},
			plan.FieldSel{Alias: "b", Column: places.Table.Column("area"), Args: map[string]any{"buffer": d, "simplify": math.Abs(x), "precision": digits}},
			plan.FieldSel{Alias: "c", Pseudo: places.Pseudo["area_centroid"], Args: map[string]any{"buffer": x, "precision": digits}})
		p.Filter = plan.And{Terms: []plan.Expr{
			cond(loc, "st_intersects", wkt),
			cond(loc, "bbox_intersects", []any{x, x, x, x}),
			cond(loc, "st_dwithin", []any{wkt, d}),
			cond(loc, "bbox_leftof", wkt),
			cond(loc, "st_exactlyequals", wkt),
			cond(loc, "bbox_intersectsnd", wkt),
			cond(loc, "bbox_intersects2d", wkt),
			cond(fx.col("places", "area"), "bbox_intersects2d", wkt),
		}}
		p.Order = []plan.OrderTerm{{Distance: loc}}
		p.Near = wkt
		stmt, err := fx.c.Compile(context.Background(), &compile.Request{Plan: p, Built: fx.built})
		if err != nil {
			// Malformed geometry is rejected at compile time by design; only
			// a successful compile is subject to the invariant.
			return nil, false
		}
		r := list(tiles)
		r.Select = fields(tiles, "id")
		r.Select.Fields = append(r.Select.Fields,
			plan.FieldSel{Pseudo: tiles.Pseudo["rast_width"]},
			plan.FieldSel{Pseudo: tiles.Pseudo["rast_envelope"]},
			plan.FieldSel{Pseudo: tiles.Pseudo["rast_envelope"], Alias: "e2", Args: map[string]any{"precision": int64(len(wkt) % 16)}},
			plan.FieldSel{Pseudo: tiles.Pseudo["rast_value"]},
			plan.FieldSel{Pseudo: tiles.Pseudo["rast_value"], Alias: "v2", Args: map[string]any{"band": int64(x)}},
			plan.FieldSel{Pseudo: tiles.Pseudo["rast_summary_stats"], Args: map[string]any{"band": int64(d)}})
		r.Filter = plan.Or{Terms: []plan.Expr{
			cond(rast, plan.OpRasterIntersects, wkt),
			plan.Not{Term: cond(rast, plan.OpRasterIntersects, wkt)},
			cond(rast, plan.OpIs, nil),
		}}
		r.Near = wkt
		raster, err := fx.c.Compile(context.Background(), &compile.Request{Plan: r, Built: fx.built})
		if err != nil {
			return nil, false
		}
		return &compile.Statement{SQL: stmt.SQL + "\n" + raster.SQL, Args: append(stmt.Args, raster.Args...)}, true
	}
	baseline, ok := compileWith(f, "POINT(1 2)", 1.5, 100)
	if !ok {
		f.Fatal("baseline spatial query failed to compile")
	}
	for _, form := range []string{"SRID=4326;POINT(1 2)", "SRID=3857;POINT(1 2)"} {
		if stmt, ok := compileWith(f, form, 1.5, 100); !ok || stmt.SQL != baseline.SQL {
			f.Fatalf("%s compiled to different SQL than bare WKT", form)
		}
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

// duplicateKey reports the user error of two select items answering under
// the same response key.
func duplicateKey(err error) bool {
	var ue *compile.UserError
	return errors.As(err, &ue) && ue.Code == compile.CodeInvalidSelect && strings.HasPrefix(ue.Detail, "two select items answer under")
}

func modCond(c plan.Cond, m plan.OpModifier) plan.Cond {
	c.Mod = m
	return c
}
