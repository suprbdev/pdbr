package compile_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/contrib"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"
	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/inflect"
	"github.com/suprbdev/pdbr/internal/plan"
)

func build(t testing.TB) *api.Built {
	t.Helper()
	return buildCatalog(t, testutil.FixtureCatalog(), true)
}

func buildCatalog(t testing.TB, cat *introspect.Catalog, etags bool) *api.Built {
	t.Helper()
	b := api.New(cat, inflect.Default, api.Options{
		FilterIndexedOnly: true,
		Functions:         true,
		BasePath:          "/api",
		Schemas:           []string{"public"},
		MaxPageSize:       100,
		Etags:             etags,
		Version:           "test",
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	built, err := b.Build()
	if err != nil {
		t.Fatalf("build api: %v", err)
	}
	return built
}

type fixture struct {
	t     testing.TB
	built *api.Built
	c     *compile.Compiler
}

func newFixture(t testing.TB) *fixture {
	built := build(t)
	return &fixture{t: t, built: built, c: compile.New(built)}
}

func (f *fixture) res(name string) *api.Resource {
	r := f.built.ByPath[name]
	if r == nil {
		f.t.Fatalf("no resource %q", name)
	}
	return r
}

func (f *fixture) col(res, name string) *introspect.Column {
	c := f.res(res).Table.Column(name)
	if c == nil {
		f.t.Fatalf("no column %s.%s", res, name)
	}
	return c
}

func (f *fixture) rel(res, key, hint string) *api.Relation {
	rel, cands := f.res(res).Resolve(key, hint)
	if rel == nil {
		f.t.Fatalf("relation %s.%s (hint %q) not resolved: %d candidates", res, key, hint, len(cands))
	}
	return rel
}

func (f *fixture) computed(res, key string) *api.Computed {
	c := f.res(res).Computed[key]
	if c == nil {
		f.t.Fatalf("no computed %s.%s", res, key)
	}
	return c
}

func (f *fixture) compile(p *plan.Request) *compile.Statement {
	f.t.Helper()
	stmt, err := f.c.Compile(context.Background(), &compile.Request{Plan: p, Built: f.built, MaxEmbedDepth: 5, MaxPageSize: 100})
	if err != nil {
		f.t.Fatalf("compile: %v", err)
	}
	return stmt
}

func (f *fixture) compileErr(p *plan.Request) error {
	f.t.Helper()
	_, err := f.c.Compile(context.Background(), &compile.Request{Plan: p, Built: f.built, MaxEmbedDepth: 5, MaxPageSize: 100})
	if err == nil {
		f.t.Fatalf("expected an error")
	}
	return err
}

func list(r *api.Resource) *plan.Request {
	return &plan.Request{Method: plan.GET, Resource: r, Page: plan.Page{Limit: 100}}
}

func fields(r *api.Resource, names ...string) *plan.Node {
	n := &plan.Node{Resource: r}
	for _, name := range names {
		n.Fields = append(n.Fields, plan.FieldSel{Column: r.Table.Column(name)})
	}
	return n
}

func cond(c *introspect.Column, op plan.Op, v any) plan.Cond {
	return plan.Cond{Column: c, Op: op, Value: v}
}

func wantSQL(t *testing.T, got *compile.Statement, wantSQL string, wantArgs ...any) {
	t.Helper()
	if got.SQL != wantSQL {
		t.Errorf("SQL mismatch\n--- want ---\n%s\n--- got ---\n%s", wantSQL, got.SQL)
	}
	if len(got.Args) != len(wantArgs) {
		t.Fatalf("args: want %v, got %v", wantArgs, got.Args)
	}
	for i := range wantArgs {
		if !argEqual(got.Args[i], wantArgs[i]) {
			t.Errorf("arg %d: want %#v, got %#v", i+1, wantArgs[i], got.Args[i])
		}
	}
}

func argEqual(a, b any) bool {
	if x, ok := a.([]any); ok {
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !argEqual(x[i], y[i]) {
				return false
			}
		}
		return true
	}
	return a == b
}

func userErr(t *testing.T, err error, code string) {
	t.Helper()
	var ue *compile.UserError
	if !errors.As(err, &ue) {
		t.Fatalf("want UserError %s, got %T %v", code, err, err)
	}
	if ue.Code != code {
		t.Fatalf("want code %s, got %s (%s)", code, ue.Code, ue.Detail)
	}
}

// ---- lists ----

const usersRowJSON = `json_build_object('id', t0."id", 'email', t0."email", 'full_name', t0."full_name", 'mood', t0."mood", 'settings', t0."settings", 'tags', to_json(t0."tags"), 'balance', (t0."balance")::text, 'created_at', t0."created_at", 'address', CASE WHEN (t0."address")::text IS NULL THEN NULL ELSE json_build_object('street', (t0."address")."street", 'city', (t0."address")."city", 'mood', (t0."address")."mood") END, 'prev_addresses', CASE WHEN t0."prev_addresses" IS NULL THEN NULL ELSE (SELECT coalesce(json_agg(CASE WHEN ((t0."prev_addresses")[e1.o])::text IS NULL THEN NULL ELSE json_build_object('street', ((t0."prev_addresses")[e1.o])."street", 'city', ((t0."prev_addresses")[e1.o])."city", 'mood', ((t0."prev_addresses")[e1.o])."mood") END ORDER BY e1.o), '[]'::json) FROM (SELECT generate_subscripts(t0."prev_addresses", 1) AS o) e1) END, 'moods', to_json(t0."moods"))`

func TestList(t *testing.T) {
	f := newFixture(t)
	stmt := f.compile(list(f.res("users")))
	wantSQL(t, stmt, `SELECT json_build_object('has_next', coalesce(bool_or(s2.__rn > $1), false), 'has_prev', false, 'first', (json_agg(s2.__key ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $1))->0, 'last', (json_agg(s2.__key ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $1))->-1, 'n', count(*) FILTER (WHERE s2.__rn <= $1), 'rows', coalesce(json_agg(s2.data ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $1), '[]'::json)) AS data
FROM (
  SELECT `+usersRowJSON+` AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."users" t0
  ORDER BY t0."id" ASC
  LIMIT 101
) s2`, 100)
	if stmt.Shape != compile.ShapePage || !stmt.Cursors || stmt.Total || stmt.Mutation {
		t.Errorf("shape metadata: %+v", stmt)
	}
}

func TestListSelectOffset(t *testing.T) {
	f := newFixture(t)
	users := f.res("users")
	p := list(users)
	p.Select = fields(users, "id", "email")
	p.Select.Fields = append(p.Select.Fields, plan.FieldSel{Alias: "n", Column: f.col("users", "full_name")})
	p.Page = plan.Page{Limit: 20, Offset: 40}
	stmt := f.compile(p)
	wantSQL(t, stmt, `SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $3), false), 'has_prev', true, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $3))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $3))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $3), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $3), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'email', t0."email", $1::text, t0."full_name") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."users" t0
  ORDER BY t0."id" ASC
  LIMIT 21
  OFFSET $2
) s1`, "n", 40, 60)
}

func TestListStarPlusComputed(t *testing.T) {
	f := newFixture(t)
	users := f.res("users")
	p := list(users)
	p.Select = &plan.Node{Resource: users, Star: true, Fields: []plan.FieldSel{
		{Column: f.col("users", "id")}, // duplicate of * → skipped
		{Computed: f.computed("users", "post_count")},
	}}
	stmt := f.compile(p)
	if !strings.Contains(stmt.SQL, `'post_count', ("public"."users_post_count"("u" => ROW(t0."id", t0."email", t0."full_name", t0."mood", t0."settings", t0."tags", t0."balance", t0."created_at", t0."address", t0."prev_addresses", t0."moods")::"public"."users"))::text`) {
		t.Errorf("computed column missing:\n%s", stmt.SQL)
	}
	if strings.Count(stmt.SQL, `'id', t0."id"`) != 1 {
		t.Errorf("duplicate id key:\n%s", stmt.SQL)
	}
}

func TestOffsetView(t *testing.T) {
	f := newFixture(t)
	metrics := f.res("metrics")
	p := list(metrics)
	p.Page = plan.Page{Limit: 10, Offset: 10}
	p.Order = []plan.OrderTerm{{Column: f.col("metrics", "value"), Desc: true}}
	stmt := f.compile(p)
	wantSQL(t, stmt, `SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $2), false), 'has_prev', true, 'n', count(*) FILTER (WHERE s1.__rn <= $2), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $2), '[]'::json)) AS data
FROM (
  SELECT json_build_object('name', t0."name", 'value', t0."value") AS data, row_number() OVER (ORDER BY t0."value" DESC NULLS LAST) AS __rn
  FROM "public"."metrics" t0
  ORDER BY t0."value" DESC NULLS LAST
  LIMIT 11
  OFFSET $1
) s1`, 10, 20)
	if stmt.Cursors {
		t.Error("view must not emit cursors")
	}
	p.Page.After = []any{1}
	userErr(t, f.compileErr(p), compile.CodeKeysetUnsupported)
}

func TestPageSizeRejected(t *testing.T) {
	f := newFixture(t)
	p := list(f.res("users"))
	p.Page.Limit = 101
	userErr(t, f.compileErr(p), compile.CodePageSizeExceeded)
	p = list(f.res("users"))
	p.Select = &plan.Node{Resource: f.res("users"), Fields: []plan.FieldSel{{Column: f.col("users", "id")}}, Embeds: []plan.Embed{{
		Key: "posts", Relation: f.rel("users", "posts", ""), Page: plan.Page{Limit: 500, LimitSet: true},
	}}}
	userErr(t, f.compileErr(p), compile.CodePageSizeExceeded)
}

// ---- embeds ----

func TestEmbedForward(t *testing.T) {
	f := newFixture(t)
	posts := f.res("posts")
	p := list(posts)
	p.Select = &plan.Node{Resource: posts, Fields: []plan.FieldSel{{Column: f.col("posts", "id")}, {Column: f.col("posts", "title")}}, Embeds: []plan.Embed{{
		Key: "users", Alias: "author", Relation: f.rel("posts", "users", ""),
		Node: fields(f.res("users"), "id", "email"),
	}}}
	stmt := f.compile(p)
	wantSQL(t, stmt, `SELECT json_build_object('has_next', coalesce(bool_or(s3.__rn > $2), false), 'has_prev', false, 'first', (json_agg(s3.__key ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $2))->0, 'last', (json_agg(s3.__key ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $2))->-1, 'n', count(*) FILTER (WHERE s3.__rn <= $2), 'rows', coalesce(json_agg(s3.data ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $2), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'title', t0."title", $1::text, l2.data) AS data, jsonb_build_array(t0."id") AS __key, t0.__rn
  FROM (
    SELECT t0."id", t0."author_id", t0."title", row_number() OVER (ORDER BY t0."id" ASC) AS __rn
    FROM "public"."posts" t0
    ORDER BY t0."id" ASC
    LIMIT 101
  ) t0
  LEFT JOIN LATERAL (
    SELECT json_build_object('id', t1."id", 'email', t1."email") AS data
    FROM "public"."users" t1
    WHERE t1."id" = t0."author_id"
    LIMIT 1
  ) l2 ON true
) s3`, "author", 100)
}

func TestEmbedBackwardPagedNested(t *testing.T) {
	f := newFixture(t)
	users := f.res("users")
	posts := f.res("posts")
	p := list(users)
	p.Page = plan.Page{Limit: 5}
	p.Select = &plan.Node{Resource: users, Fields: []plan.FieldSel{{Column: f.col("users", "id")}}, Embeds: []plan.Embed{{
		Key: "posts", Relation: f.rel("users", "posts", ""),
		Node: &plan.Node{Resource: posts, Fields: []plan.FieldSel{{Column: f.col("posts", "id")}, {Column: f.col("posts", "title")}}, Embeds: []plan.Embed{{
			Key: "comments", Relation: f.rel("posts", "comments", ""),
			Node: fields(f.res("comments"), "id"),
			Page: plan.Page{Limit: 3, LimitSet: true},
		}}},
		Filter: cond(f.col("posts", "published"), plan.OpIs, true),
		Order:  []plan.OrderTerm{{Column: f.col("posts", "title"), Desc: true}},
		Page:   plan.Page{Limit: 5, Offset: 5, LimitSet: true},
	}}}
	stmt := f.compile(p)
	wantSQL(t, stmt, `SELECT json_build_object('has_next', coalesce(bool_or(s7.__rn > $2), false), 'has_prev', false, 'first', (json_agg(s7.__key ORDER BY s7.__rn ASC) FILTER (WHERE s7.__rn <= $2))->0, 'last', (json_agg(s7.__key ORDER BY s7.__rn ASC) FILTER (WHERE s7.__rn <= $2))->-1, 'n', count(*) FILTER (WHERE s7.__rn <= $2), 'rows', coalesce(json_agg(s7.data ORDER BY s7.__rn ASC) FILTER (WHERE s7.__rn <= $2), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'posts', l6.data) AS data, jsonb_build_array(t0."id") AS __key, t0.__rn
  FROM (
    SELECT t0."id", row_number() OVER (ORDER BY t0."id" ASC) AS __rn
    FROM "public"."users" t0
    ORDER BY t0."id" ASC
    LIMIT 6
  ) t0
  LEFT JOIN LATERAL (
    SELECT coalesce(json_agg(s5.data), '[]'::json) AS data
    FROM (
      SELECT json_build_object('id', t1."id", 'title', t1."title", 'comments', l4.data) AS data
      FROM "public"."posts" t1
      LEFT JOIN LATERAL (
        SELECT coalesce(json_agg(s3.data), '[]'::json) AS data
        FROM (
          SELECT json_build_object('id', t2."id") AS data
          FROM "public"."comments" t2
          WHERE t2."post_id" = t1."id"
          ORDER BY t2."id" ASC
          LIMIT 3
        ) s3
      ) l4 ON true
      WHERE t1."author_id" = t0."id" AND t1."published" IS TRUE
      ORDER BY t1."title" DESC, t1."id" ASC
      LIMIT 5
      OFFSET $1
    ) s5
  ) l6 ON true
) s7`, 5, 5)
}

func TestEmbedInner(t *testing.T) {
	f := newFixture(t)
	users := f.res("users")
	p := list(users)
	p.Count = true
	p.Select = &plan.Node{Resource: users, Fields: []plan.FieldSel{{Column: f.col("users", "id")}}, Embeds: []plan.Embed{{
		Key: "posts", Relation: f.rel("users", "posts", ""), Inner: true,
		Node:   fields(f.res("posts"), "id"),
		Filter: cond(f.col("posts", "title"), plan.OpSw, "A"),
	}}}
	stmt := f.compile(p)
	if !strings.Contains(stmt.SQL, `WHERE EXISTS (SELECT 1 FROM "public"."posts" r4 WHERE r4."author_id" = t0."id" AND r4."title" LIKE $2 || '%')`) {
		t.Errorf("inner EXISTS missing:\n%s", stmt.SQL)
	}
	if !strings.Contains(stmt.SQL, `WHERE t1."author_id" = t0."id" AND t1."title" LIKE $1 || '%'`) {
		t.Errorf("lateral filter missing:\n%s", stmt.SQL)
	}
	if !strings.Contains(stmt.SQL, `'total', (SELECT count(*) FROM "public"."users" c6 WHERE EXISTS (SELECT 1 FROM "public"."posts" r7 WHERE r7."author_id" = c6."id" AND r7."title" LIKE $4 || '%')), 'n', `) {
		t.Errorf("count must repeat the inner restriction:\n%s", stmt.SQL)
	}
	if len(stmt.Args) != 4 {
		t.Errorf("args: %v", stmt.Args)
	}
	if !stmt.Total {
		t.Error("Total flag")
	}
}

func TestEmbedHintsSelfRef(t *testing.T) {
	f := newFixture(t)
	comments := f.res("comments")
	p := list(comments)
	p.Select = &plan.Node{Resource: comments, Fields: []plan.FieldSel{{Column: f.col("comments", "id")}}, Embeds: []plan.Embed{
		{Key: "comments", Alias: "parent", Relation: f.rel("comments", "comments", "parent_id"), Node: fields(comments, "id")},
		{Key: "comments", Alias: "replies", Relation: f.rel("comments", "comments", "comments_parent_id_fkey"), Node: fields(comments, "id")},
	}}
	stmt := f.compile(p)
	if !strings.Contains(stmt.SQL, `WHERE t1."id" = t0."parent_id"`) || !strings.Contains(stmt.SQL, `WHERE t3."parent_id" = t0."id"`) {
		t.Errorf("self-reference embeds:\n%s", stmt.SQL)
	}
	if !strings.Contains(stmt.SQL, `$1::text, l2.data, $2::text, l5.data`) {
		t.Errorf("aliases:\n%s", stmt.SQL)
	}
	if stmt.Args[0] != "parent" || stmt.Args[1] != "replies" {
		t.Errorf("alias args %v", stmt.Args)
	}
}

func TestEmbedDepth(t *testing.T) {
	f := newFixture(t)
	comments := f.res("comments")
	rel := f.rel("comments", "comments", "comments_parent_id_fkey")
	node := fields(comments, "id")
	for range 6 {
		node = &plan.Node{Resource: comments, Embeds: []plan.Embed{{Key: "comments", Relation: rel, Node: node}}}
	}
	p := list(comments)
	p.Select = node
	userErr(t, f.compileErr(p), compile.CodeEmbedDepthExceeded)
}

// ---- filters ----

func TestFilterOps(t *testing.T) {
	f := newFixture(t)
	users := f.res("users")
	email := f.col("users", "email")
	id := f.col("users", "id")
	tags := f.col("users", "tags")
	settings := f.col("users", "settings")
	cases := []struct {
		name string
		cond plan.Cond
		sql  string
		args []any
	}{
		{"eq", cond(email, plan.OpEq, "a@b.c"), `t0."email" = $1`, []any{"a@b.c"}},
		{"neq", cond(email, plan.OpNeq, "a@b.c"), `t0."email" <> $1`, []any{"a@b.c"}},
		{"not.eq", plan.Cond{Column: email, Op: plan.OpEq, Value: "x", Negate: true}, `NOT (t0."email" = $1)`, []any{"x"}},
		{"gt", cond(id, plan.OpGt, int64(3)), `t0."id" > $1`, []any{int64(3)}},
		{"gte", cond(id, plan.OpGte, 3), `t0."id" >= $1`, []any{3}},
		{"lt", cond(id, plan.OpLt, 3), `t0."id" < $1`, []any{3}},
		{"lte", cond(id, plan.OpLte, 3), `t0."id" <= $1`, []any{3}},
		{"in", cond(id, plan.OpIn, []any{1, 2, 3}), `t0."id" = ANY($1::"int4"[])`, []any{[]any{1, 2, 3}}},
		{"not.in", plan.Cond{Column: email, Op: plan.OpIn, Value: []any{"a", "b"}, Negate: true}, `t0."email" <> ALL($1::"text"[])`, []any{[]any{"a", "b"}}},
		{"is.null", cond(email, plan.OpIs, nil), `t0."email" IS NULL`, nil},
		{"not.is.null", plan.Cond{Column: email, Op: plan.OpIs, Value: nil, Negate: true}, `t0."email" IS NOT NULL`, nil},
		{"is.true", cond(f.col("posts", "published"), plan.OpIs, true), `t0."published" IS TRUE`, nil},
		{"is.false-string", cond(f.col("posts", "published"), plan.OpIs, "false"), `t0."published" IS FALSE`, nil},
		{"like", cond(email, plan.OpLike, "*@example.com"), `t0."email" LIKE $1`, []any{"%@example.com"}},
		{"ilike", cond(email, plan.OpILike, "A*"), `t0."email" ILIKE $1`, []any{"A%"}},
		{"sw", cond(email, plan.OpSw, "a_%\\"), `t0."email" LIKE $1 || '%'`, []any{`a\_\%\\`}},
		{"ew", cond(email, plan.OpEw, "x"), `t0."email" LIKE '%' || $1`, []any{"x"}},
		{"cs-array", cond(tags, plan.OpCs, []any{"admin", "ops"}), `t0."tags" @> $1::"text"[]`, []any{[]any{"admin", "ops"}}},
		{"cs-array-literal", cond(tags, plan.OpCs, "{admin,ops}"), `t0."tags" @> $1::"text"[]`, []any{"{admin,ops}"}},
		{"cd-array", cond(tags, plan.OpCd, []any{"a"}), `t0."tags" <@ $1::"text"[]`, []any{[]any{"a"}}},
		{"ov", cond(tags, plan.OpOv, []any{"ops"}), `t0."tags" && $1::"text"[]`, []any{[]any{"ops"}}},
		{"cs-jsonb-raw", cond(settings, plan.OpCs, `{"theme":"dark"}`), `t0."settings" @> $1::jsonb`, []any{`{"theme":"dark"}`}},
		{"cs-jsonb-decoded", cond(settings, plan.OpCs, map[string]any{"theme": "dark"}), `t0."settings" @> $1::jsonb`, []any{`{"theme":"dark"}`}},
		{"cd-jsonb", cond(settings, plan.OpCd, `{}`), `t0."settings" <@ $1::jsonb`, []any{`{}`}},
		{"hk", cond(settings, plan.OpHk, "theme"), `t0."settings" ? $1`, []any{"theme"}},
		{"jpe", cond(settings, plan.OpJpe, `$.a[*]?(@=="b")`), `t0."settings" @? $1::jsonpath`, []any{`$.a[*]?(@=="b")`}},
		{"jpm", cond(settings, plan.OpJpm, `$.theme=="dark"`), `t0."settings" @@ $1::jsonpath`, []any{`$.theme=="dark"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := users
			if tc.cond.Column.Name == "published" {
				r = f.res("posts")
			}
			p := list(r)
			p.Select = fields(r, "id")
			p.Filter = tc.cond
			stmt := f.compile(p)
			if !strings.Contains(stmt.SQL, "\n  WHERE "+tc.sql+"\n") {
				t.Errorf("want predicate %q in:\n%s", tc.sql, stmt.SQL)
			}
			// args: predicate params first, then the trim.
			if len(stmt.Args) != len(tc.args)+1 {
				t.Fatalf("args: %v", stmt.Args)
			}
			for i, want := range tc.args {
				if !argEqual(stmt.Args[i], want) {
					t.Errorf("arg %d: want %#v got %#v", i+1, want, stmt.Args[i])
				}
			}
		})
	}
}

func TestFilterJSONColumnCast(t *testing.T) {
	f := newFixture(t)
	cat := testutil.FixtureCatalog()
	users := cat.Table("public", "users")
	users.Columns = append(users.Columns, &introspect.Column{Name: "prefs", Position: 12, PGType: "json", TypeSchema: "pg_catalog"})
	built := buildCatalog(t, cat, true)
	r := built.ByPath["users"]
	p := list(r)
	p.Select = fields(r, "id")
	p.Filter = plan.And{Terms: []plan.Expr{
		cond(r.Table.Column("prefs"), plan.OpHk, "k"),
		cond(r.Table.Column("prefs"), plan.OpJpe, "$.k"),
	}}
	stmt, err := compile.New(built).Compile(context.Background(), &compile.Request{Plan: p, Built: built})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stmt.SQL, `WHERE t0."prefs"::jsonb ? $1 AND t0."prefs"::jsonb @? $2::jsonpath`) {
		t.Errorf("json column must be cast:\n%s", stmt.SQL)
	}
	_ = f
}

func TestFilterEnum(t *testing.T) {
	f := newFixture(t)
	users := f.res("users")
	mood := f.col("users", "mood")
	moods := f.col("users", "moods")
	events := f.res("events")
	p := list(users)
	p.Select = fields(users, "id")
	p.Filter = plan.And{Terms: []plan.Expr{
		cond(mood, plan.OpEq, "happy"),
		cond(mood, plan.OpIn, []any{"happy", "ok"}),
		cond(moods, plan.OpCs, []any{"sad"}),
	}}
	stmt := f.compile(p)
	if !strings.Contains(stmt.SQL, `WHERE t0."mood" = $1 AND t0."mood" = ANY($2::text[]::"public"."mood"[]) AND t0."moods" @> $3::text[]::"public"."mood"[]`) {
		t.Errorf("enum casts:\n%s", stmt.SQL)
	}
	// table-backed enum: plain text, never cast to a type that does not exist.
	p = list(events)
	p.Select = fields(events, "id")
	p.Filter = cond(f.col("events", "type_code"), plan.OpIn, []any{"a", "b"})
	stmt = f.compile(p)
	if !strings.Contains(stmt.SQL, `WHERE t0."type_code" = ANY($1::text[])`) {
		t.Errorf("table-backed enum:\n%s", stmt.SQL)
	}
}

func TestFilterLogicSorted(t *testing.T) {
	f := newFixture(t)
	users := f.res("users")
	mood := f.col("users", "mood")
	email := f.col("users", "email")
	tags := f.col("users", "tags")
	settings := f.col("users", "settings")
	tree := plan.And{Terms: []plan.Expr{
		plan.Not{Term: cond(settings, plan.OpHk, "banned")},
		plan.Or{Terms: []plan.Expr{cond(tags, plan.OpCs, []any{"admin"}), cond(email, plan.OpEw, "@example.com")}},
		cond(mood, plan.OpIn, []any{"happy", "ok"}),
	}}
	p := list(users)
	p.Select = fields(users, "id")
	p.Filter = tree
	stmt := f.compile(p)
	want := `WHERE t0."mood" = ANY($1::text[]::"public"."mood"[]) AND NOT (t0."settings" ? $2) AND (t0."email" LIKE '%' || $3 OR t0."tags" @> $4::"text"[])`
	if !strings.Contains(stmt.SQL, want) {
		t.Errorf("logic tree:\nwant %s\n%s", want, stmt.SQL)
	}
	// Same set in a different order → identical SQL and args.
	tree2 := plan.And{Terms: []plan.Expr{
		cond(mood, plan.OpIn, []any{"happy", "ok"}),
		plan.Or{Terms: []plan.Expr{cond(email, plan.OpEw, "@example.com"), cond(tags, plan.OpCs, []any{"admin"})}},
		plan.Not{Term: cond(settings, plan.OpHk, "banned")},
	}}
	p.Filter = tree2
	stmt2 := f.compile(p)
	if stmt2.SQL != stmt.SQL {
		t.Errorf("filter order changed SQL:\n%s\n---\n%s", stmt.SQL, stmt2.SQL)
	}
	// not.or at the top level.
	p.Filter = plan.Not{Term: plan.Or{Terms: []plan.Expr{cond(email, plan.OpSw, "Draft"), cond(mood, plan.OpIs, nil)}}}
	stmt = f.compile(p)
	if !strings.Contains(stmt.SQL, `WHERE NOT ((t0."email" LIKE $1 || '%' OR t0."mood" IS NULL))`) {
		t.Errorf("not.or:\n%s", stmt.SQL)
	}
}

func TestFilterRelation(t *testing.T) {
	f := newFixture(t)
	users := f.res("users")
	posts := f.rel("users", "posts", "")
	title := f.col("posts", "title")
	leaf := cond(title, plan.OpEq, "x")
	tr := true
	fa := false
	cases := []struct {
		name string
		rp   plan.RelationPath
		want string
	}{
		{"some", plan.RelationPath{Steps: []*api.Relation{posts}, Leaf: &leaf}, `WHERE EXISTS (SELECT 1 FROM "public"."posts" r1 WHERE r1."author_id" = t0."id" AND (r1."title" = $1))`},
		{"none", plan.RelationPath{Steps: []*api.Relation{posts}, Quantifier: plan.None, Leaf: &leaf}, `WHERE NOT EXISTS (SELECT 1 FROM "public"."posts" r1 WHERE r1."author_id" = t0."id" AND (r1."title" = $1))`},
		{"every", plan.RelationPath{Steps: []*api.Relation{posts}, Quantifier: plan.Every, Leaf: &leaf}, `WHERE NOT EXISTS (SELECT 1 FROM "public"."posts" r1 WHERE r1."author_id" = t0."id" AND NOT (r1."title" = $1))`},
		{"exists.true", plan.RelationPath{Steps: []*api.Relation{posts}, Quantifier: plan.Exists, Exists: &tr}, `WHERE EXISTS (SELECT 1 FROM "public"."posts" r1 WHERE r1."author_id" = t0."id")`},
		{"exists.false", plan.RelationPath{Steps: []*api.Relation{posts}, Quantifier: plan.Exists, Exists: &fa}, `WHERE NOT EXISTS (SELECT 1 FROM "public"."posts" r1 WHERE r1."author_id" = t0."id")`},
		{"filter-anded", plan.RelationPath{Steps: []*api.Relation{posts}, Filter: plan.And{Terms: []plan.Expr{leaf, cond(f.col("posts", "published"), plan.OpIs, true)}}}, `WHERE EXISTS (SELECT 1 FROM "public"."posts" r1 WHERE r1."author_id" = t0."id" AND ((r1."published" IS TRUE AND r1."title" = $1)))`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := list(users)
			p.Select = fields(users, "id")
			rp := tc.rp
			p.Filter = plan.Cond{Relation: &rp, Op: plan.OpExists}
			stmt := f.compile(p)
			if !strings.Contains(stmt.SQL, tc.want) {
				t.Errorf("want %s\n%s", tc.want, stmt.SQL)
			}
		})
	}
	// forward relation filter from posts to users; quantifier rejected.
	pr := f.res("posts")
	p := list(pr)
	p.Select = fields(pr, "id")
	email := cond(f.col("users", "email"), plan.OpEq, "x")
	p.Filter = plan.Cond{Relation: &plan.RelationPath{Steps: []*api.Relation{f.rel("posts", "users", "")}, Leaf: &email}}
	stmt := f.compile(p)
	if !strings.Contains(stmt.SQL, `WHERE EXISTS (SELECT 1 FROM "public"."users" r1 WHERE r1."id" = t0."author_id" AND (r1."email" = $1))`) {
		t.Errorf("forward relation filter:\n%s", stmt.SQL)
	}
	p.Filter = plan.Cond{Relation: &plan.RelationPath{Steps: []*api.Relation{f.rel("posts", "users", "")}, Quantifier: plan.None, Leaf: &email}}
	userErr(t, f.compileErr(p), compile.CodeInvalidFilter)
	// two steps: users -> posts -> comments.
	body := cond(f.col("comments", "body"), plan.OpEq, "hi")
	p = list(users)
	p.Select = fields(users, "id")
	p.Filter = plan.Cond{Relation: &plan.RelationPath{Steps: []*api.Relation{posts, f.rel("posts", "comments", "")}, Leaf: &body}}
	stmt = f.compile(p)
	if !strings.Contains(stmt.SQL, `WHERE EXISTS (SELECT 1 FROM "public"."posts" r1 WHERE r1."author_id" = t0."id" AND (EXISTS (SELECT 1 FROM "public"."comments" r2 WHERE r2."post_id" = r1."id" AND (r2."body" = $1))))`) {
		t.Errorf("nested relation filter:\n%s", stmt.SQL)
	}
}

func TestFilterComputed(t *testing.T) {
	f := newFixture(t)
	users := f.res("users")
	p := list(users)
	p.Select = fields(users, "id")
	p.Filter = plan.Cond{Computed: f.computed("users", "post_count"), Op: plan.OpGt, Value: 3}
	stmt := f.compile(p)
	if !strings.Contains(stmt.SQL, `WHERE "public"."users_post_count"("u" => ROW(t0."id", t0."email", t0."full_name", t0."mood", t0."settings", t0."tags", t0."balance", t0."created_at", t0."address", t0."prev_addresses", t0."moods")::"public"."users") > $1`) {
		t.Errorf("computed filter:\n%s", stmt.SQL)
	}
}

func TestFilterSpatial(t *testing.T) {
	f := newFixture(t)
	places := f.res("places")
	loc := f.col("places", "location")
	area := f.col("places", "area")
	const (
		geom4326    = `CASE WHEN left($1, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($1), 4326) ELSE ST_GeomFromEWKT($1) END`
		geogOperand = `CASE WHEN left($1, 1) = '{' THEN ST_GeomFromGeoJSON($1) ELSE ST_GeomFromEWKT($1) END`
	)
	cases := []struct {
		name string
		cond plan.Cond
		want string
		n    int
		arg  any
	}{
		{"intersects-wkt", cond(loc, "st_intersects", "POINT(1 2)"), `WHERE ST_Intersects(t0."location", ` + geom4326 + `)`, 1, "SRID=4326;POINT(1 2)"},
		{"intersects-ewkt", cond(loc, "st_intersects", "SRID=3857;POINT(1 2)"), `WHERE ST_Intersects(t0."location", ` + geom4326 + `)`, 1, "SRID=3857;POINT(1 2)"},
		{"within-geojson", cond(loc, "st_within", map[string]any{"type": "Point", "coordinates": []any{1.0, 2.0}}), `WHERE ST_Within(t0."location", ` + geom4326 + `)`, 1, `{"coordinates":[1,2],"type":"Point"}`},
		{"not.within", plan.Cond{Column: loc, Op: "st_within", Value: "POINT(1 2)", Negate: true}, `WHERE NOT (ST_Within(t0."location", ` + geom4326 + `))`, 1, "SRID=4326;POINT(1 2)"},
		{"dwithin-list", cond(loc, "st_dwithin", []any{"POINT(1 2)", "5000", "true"}), `WHERE ST_DWithin(t0."location"::geography, (` + geom4326 + `)::geography, $2)`, 2, "SRID=4326;POINT(1 2)"},
		{"dwithin-geog", cond(area, "st_dwithin", []any{"POINT(1 2)", 5000.0}), `WHERE ST_DWithin(t0."area", (` + geogOperand + `)::geography, $2)`, 2, "SRID=4326;POINT(1 2)"},
		{"beyond", cond(loc, "st_beyond", map[string]any{"geometry": "POINT(1 2)", "distance": 1}), `WHERE NOT ST_DWithin(t0."location", ` + geom4326 + `, $2)`, 2, "SRID=4326;POINT(1 2)"},
		{"bbox", cond(loc, "bbox_intersects", []any{"-0.5", "51.3", "0.3", "51.7"}), `WHERE t0."location" && ST_MakeEnvelope($1, $2, $3, $4, 4326)`, 4, nil},
		{"bbox-srid", cond(loc, "bbox_containedby", []any{1, 2, 3, 4, 3857}), `WHERE t0."location" @ ST_MakeEnvelope($1, $2, $3, $4, 3857)`, 4, nil},
		// ~ / @ have no geography overload: both sides fall back to geometry.
		{"bbox-geog", cond(area, "bbox_contains", []any{1, 2, 3, 4}), `WHERE t0."area"::geometry ~ ST_MakeEnvelope($1, $2, $3, $4, 4326)`, 4, nil},
		{"bbox-geog-and", cond(area, "bbox_intersects", []any{1, 2, 3, 4}), `WHERE t0."area" && (ST_MakeEnvelope($1, $2, $3, $4, 4326))::geography`, 4, nil},
		{"within-geog", cond(area, "st_within", "POINT(1 2)"), `WHERE ST_Within(t0."area"::geometry, ` + geogOperand + `)`, 1, "SRID=4326;POINT(1 2)"},
		{"is.null", cond(loc, plan.OpIs, nil), `WHERE t0."location" IS NULL`, 0, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := list(places)
			p.Select = fields(places, "id")
			p.Filter = tc.cond
			stmt := f.compile(p)
			if !strings.Contains(stmt.SQL, tc.want) {
				t.Errorf("want %s\n%s", tc.want, stmt.SQL)
			}
			if len(stmt.Args) != tc.n+1 {
				t.Errorf("args: %v", stmt.Args)
			}
			if tc.arg != nil && stmt.Args[0] != tc.arg {
				t.Errorf("operand arg = %v, want %v", stmt.Args[0], tc.arg)
			}
		})
	}
	p := list(places)
	p.Filter = cond(loc, plan.OpEq, "x")
	userErr(t, f.compileErr(p), compile.CodeInvalidFilter)
	p.Filter = cond(f.col("places", "name"), "st_intersects", "POINT(1 2)")
	userErr(t, f.compileErr(p), compile.CodeInvalidFilter)
	p.Filter = cond(loc, "st_intersects", "not a geometry")
	userErr(t, f.compileErr(p), compile.CodeInvalidFilter)
}

func TestSpatialColumnSRID(t *testing.T) {
	f := newFixture(t)
	places := f.res("places")
	merc := *f.col("places", "location")
	merc.Spatial = &introspect.SpatialType{Subtype: introspect.SpatialPoint, SRID: 3857}
	point := map[string]any{"type": "Point", "coordinates": []any{-0.1281, 51.508}}
	const merc3857 = `CASE WHEN left($1, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($1), 3857) ELSE ST_GeomFromEWKT($1) END`
	cases := []struct {
		name  string
		value any
		arg   string
	}{
		{"geojson-reprojected", point, `{"coordinates":[-0.1281,51.508],"type":"Point"}`},
		{"wkt-tagged", "POINT(1 2)", "SRID=3857;POINT(1 2)"},
		{"ewkt-kept", "SRID=4326;POINT(1 2)", "SRID=4326;POINT(1 2)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := list(places)
			p.Select = fields(places, "id")
			p.Filter = cond(&merc, "st_intersects", tc.value)
			stmt := f.compile(p)
			if !strings.Contains(stmt.SQL, `ST_Intersects(t0."location", `+merc3857+`)`) {
				t.Errorf("want %s\n%s", merc3857, stmt.SQL)
			}
			if stmt.Args[0] != tc.arg {
				t.Errorf("operand arg = %v, want %v", stmt.Args[0], tc.arg)
			}
		})
	}
	p := list(places)
	p.Select = fields(places, "id")
	p.Order = []plan.OrderTerm{{Distance: &merc}}
	p.Near = point
	if stmt := f.compile(p); !strings.Contains(stmt.SQL, `t0."location" <-> `+merc3857) {
		t.Errorf("near reprojected:\n%s", stmt.SQL)
	}
}

// ---- order ----

func TestOrderNulls(t *testing.T) {
	f := newFixture(t)
	users := f.res("users")
	p := list(users)
	p.Select = fields(users, "id")
	p.Order = []plan.OrderTerm{
		{Column: f.col("users", "mood"), Desc: true},                         // nullable desc → NULLS LAST
		{Column: f.col("users", "full_name"), NullsFirst: true},              // nullable asc nullsfirst
		{Column: f.col("users", "email"), Desc: true},                        // not null desc → plain
		{Column: f.col("users", "created_at"), Desc: true, NullsFirst: true}, // not null: modifier dropped
	}
	stmt := f.compile(p)
	if !strings.Contains(stmt.SQL, `ORDER BY t0."mood" DESC NULLS LAST, t0."full_name" ASC NULLS FIRST, t0."email" DESC, t0."created_at" DESC, t0."id" ASC`) {
		t.Errorf("order:\n%s", stmt.SQL)
	}
	// PK given explicitly is not appended twice.
	p.Order = []plan.OrderTerm{{Column: f.col("users", "id"), Desc: true}}
	stmt = f.compile(p)
	if !strings.Contains(stmt.SQL, `ORDER BY t0."id" DESC) AS __rn`) {
		t.Errorf("pk order:\n%s", stmt.SQL)
	}
}

func TestOrderRelatedComputedDistance(t *testing.T) {
	f := newFixture(t)
	posts := f.res("posts")
	p := list(posts)
	p.Select = fields(posts, "id")
	p.Order = []plan.OrderTerm{{Relation: &plan.RelationOrder{Relation: f.rel("posts", "users", ""), Column: f.col("users", "email")}}}
	stmt := f.compile(p)
	if !strings.Contains(stmt.SQL, `ORDER BY (SELECT __ro."email" FROM "public"."users" __ro WHERE __ro."id" = t0."author_id") ASC, t0."id" ASC`) {
		t.Errorf("forward related order:\n%s", stmt.SQL)
	}
	users := f.res("users")
	p = list(users)
	p.Select = fields(users, "id")
	p.Order = []plan.OrderTerm{
		{Relation: &plan.RelationOrder{Relation: f.rel("users", "posts", ""), Column: f.col("posts", "title")}, Desc: true},
		{Computed: f.computed("users", "post_count"), Desc: true},
	}
	stmt = f.compile(p)
	if !strings.Contains(stmt.SQL, `ORDER BY (SELECT MAX(__ro."title") FROM "public"."posts" __ro WHERE __ro."author_id" = t0."id") DESC NULLS LAST, "public"."users_post_count"("u" => ROW(`) {
		t.Errorf("backward related + computed order:\n%s", stmt.SQL)
	}
	places := f.res("places")
	p = list(places)
	p.Select = fields(places, "id")
	p.Order = []plan.OrderTerm{{Distance: f.col("places", "location"), NullsFirst: true}}
	p.Near = "SRID=4326;POINT(-0.1281 51.508)"
	stmt = f.compile(p)
	if !strings.Contains(stmt.SQL, `ORDER BY t0."location" <-> CASE WHEN left($1, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($1), 4326) ELSE ST_GeomFromEWKT($1) END ASC, t0."id" ASC`) {
		t.Errorf("distance order:\n%s", stmt.SQL)
	}
	if stmt.Args[0] != "SRID=4326;POINT(-0.1281 51.508)" {
		t.Errorf("near arg: %v", stmt.Args)
	}
	p.Near = nil
	userErr(t, f.compileErr(p), compile.CodeInvalidFilter)
}

// ---- keyset ----

func TestKeysetAfter(t *testing.T) {
	f := newFixture(t)
	posts := f.res("posts")
	p := list(posts)
	p.Select = fields(posts, "id", "title")
	p.Order = []plan.OrderTerm{{Column: f.col("posts", "title")}}
	p.Page = plan.Page{Limit: 2, After: []any{7}}
	stmt := f.compile(p)
	wantSQL(t, stmt, `SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $3), false), 'has_prev', true, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $3))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $3))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $3), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $3), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'title', t0."title") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."title" ASC, t0."id" ASC) AS __rn
  FROM "public"."posts" t0
  WHERE (t0."title", t0."id") > ((SELECT __a."title" FROM "public"."posts" __a WHERE __a."id" = $1), $2)
  ORDER BY t0."title" ASC, t0."id" ASC
  LIMIT 3
) s1`, 7, 7, 2)
	// Mixed directions / nullable → expanded chain; PK-only order → params only.
	p.Order = []plan.OrderTerm{{Column: f.col("posts", "body"), Desc: true}}
	stmt = f.compile(p)
	if !strings.Contains(stmt.SQL, `WHERE ((t0."body" < (SELECT __a."body" FROM "public"."posts" __a WHERE __a."id" = $1) OR (t0."body" IS NULL AND (SELECT __a."body" FROM "public"."posts" __a WHERE __a."id" = $1) IS NOT NULL)) OR (t0."body" IS NOT DISTINCT FROM (SELECT __a."body" FROM "public"."posts" __a WHERE __a."id" = $1) AND t0."id" > $2))`) {
		t.Errorf("nullable keyset:\n%s", stmt.SQL)
	}
	p.Order = []plan.OrderTerm{{Column: f.col("posts", "title")}, {Column: f.col("posts", "author_id"), Desc: true}}
	stmt = f.compile(p)
	if !strings.Contains(stmt.SQL, `WHERE t0."title" >= (SELECT __a."title" FROM "public"."posts" __a WHERE __a."id" = $1) AND (t0."title" > (SELECT __a."title" FROM "public"."posts" __a WHERE __a."id" = $1) OR (t0."title" = `) {
		t.Errorf("mixed keyset leading bound:\n%s", stmt.SQL)
	}
	p.Order = nil
	stmt = f.compile(p)
	if !strings.Contains(stmt.SQL, `WHERE t0."id" > $1`) {
		t.Errorf("pk-only keyset:\n%s", stmt.SQL)
	}
	p.Page.After = []any{1, 2}
	userErr(t, f.compileErr(p), compile.CodeInvalidCursor)
}

func TestKeysetBefore(t *testing.T) {
	f := newFixture(t)
	posts := f.res("posts")
	p := list(posts)
	p.Select = fields(posts, "id")
	p.Order = []plan.OrderTerm{{Column: f.col("posts", "title")}}
	p.Page = plan.Page{Limit: 2, Before: []any{13}}
	stmt := f.compile(p)
	wantSQL(t, stmt, `SELECT json_build_object('has_next', true, 'has_prev', coalesce(bool_or(s1.__rn > $3), false), 'first', (json_agg(s1.__key ORDER BY s1.__rn DESC) FILTER (WHERE s1.__rn <= $3))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn DESC) FILTER (WHERE s1.__rn <= $3))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $3), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn DESC) FILTER (WHERE s1.__rn <= $3), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."title" DESC, t0."id" DESC) AS __rn
  FROM "public"."posts" t0
  WHERE (t0."title", t0."id") < ((SELECT __a."title" FROM "public"."posts" __a WHERE __a."id" = $1), $2)
  ORDER BY t0."title" DESC, t0."id" DESC
  LIMIT 3
) s1`, 13, 13, 2)
	p.Page.Offset = 1
	userErr(t, f.compileErr(p), compile.CodeKeysetUnsupported)
	// nullable desc reversed → ASC NULLS FIRST
	p.Page.Offset = 0
	p.Order = []plan.OrderTerm{{Column: f.col("posts", "body"), Desc: true}}
	stmt = f.compile(p)
	if !strings.Contains(stmt.SQL, `ORDER BY t0."body" ASC NULLS FIRST, t0."id" DESC`) {
		t.Errorf("reversed nulls:\n%s", stmt.SQL)
	}
}

// ---- count / distinct ----

func TestCount(t *testing.T) {
	f := newFixture(t)
	users := f.res("users")
	p := list(users)
	p.Select = fields(users, "id")
	p.Count = true
	p.Filter = cond(f.col("users", "mood"), plan.OpEq, "happy")
	stmt := f.compile(p)
	if !strings.Contains(stmt.SQL, `'total', (SELECT count(*) FROM "public"."users" c2 WHERE c2."mood" = $3), 'n', `) {
		t.Errorf("count:\n%s", stmt.SQL)
	}
	if !stmt.Total || len(stmt.Args) != 3 {
		t.Errorf("metadata/args: %+v", stmt)
	}
}

func TestDistinct(t *testing.T) {
	f := newFixture(t)
	users := f.res("users")
	p := list(users)
	p.Select = fields(users, "mood")
	p.Distinct = []*introspect.Column{f.col("users", "mood")}
	p.Order = []plan.OrderTerm{{Column: f.col("users", "email")}, {Column: f.col("users", "mood"), Desc: true}}
	p.Count = true
	p.Page.Limit = 5
	stmt := f.compile(p)
	wantSQL(t, stmt, `SELECT json_build_object('has_next', coalesce(bool_or(s2.__rn > $1), false), 'has_prev', false, 'total', (SELECT count(*) FROM (SELECT DISTINCT c3."mood" FROM "public"."users" c3) c4), 'n', count(*) FILTER (WHERE s2.__rn <= $1), 'rows', coalesce(json_agg(s2.data ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $1), '[]'::json)) AS data
FROM (
  SELECT d1.*, row_number() OVER (ORDER BY d1.__rn0) AS __rn
  FROM (
    SELECT DISTINCT ON (t0."mood") json_build_object('mood', t0."mood") AS data, row_number() OVER (ORDER BY t0."mood" DESC NULLS LAST, t0."email" ASC, t0."id" ASC) AS __rn0
    FROM "public"."users" t0
    ORDER BY t0."mood" DESC NULLS LAST, t0."email" ASC, t0."id" ASC
    LIMIT 6
  ) d1
) s2`, 5)
	if stmt.Cursors {
		t.Error("distinct must not emit cursors")
	}
	p.Page.After = []any{1}
	userErr(t, f.compileErr(p), compile.CodeKeysetUnsupported)
}

// ---- rows ----

func TestETagHashesExposedColumnsOnly(t *testing.T) {
	b := api.New(testutil.FixtureCatalog(), inflect.Default, api.Options{
		Functions:   true,
		BasePath:    "/api",
		Schemas:     []string{"public"},
		MaxPageSize: 100,
		Etags:       true,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	users := b.Resource("public", "users")
	b.RemoveField(users, "balance", false)
	b.RemoveField(users, "mood", true)
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, built: built, c: compile.New(built)}
	users = f.res("users")
	key := &plan.Key{Constraint: users.Table.PrimaryKey, Values: []any{1}}
	row := f.compile(&plan.Request{Method: plan.GET, Resource: users, Key: key, Select: fields(users, "id")})
	const hash = `md5(to_jsonb(ROW(t0."id", t0."email", t0."full_name", t0."settings", t0."tags", t0."created_at", t0."address", t0."prev_addresses", t0."moods"))::text)`
	if !strings.Contains(row.SQL, "'etag', "+hash) {
		t.Errorf("row etag hashes hidden columns:\n%s", row.SQL)
	}
	patch := f.compile(&plan.Request{Method: plan.PATCH, Resource: users, Key: key, Body: body(users, map[string]any{"full_name": "x"}), IfMatch: `W/"abc"`})
	if !strings.Contains(patch.SQL, strings.ReplaceAll(hash, "t0.", "__cur.")+" AS h") {
		t.Errorf("If-Match hash differs from the ETag:\n%s", patch.SQL)
	}
	for _, st := range []*compile.Statement{row, patch} {
		if strings.Contains(st.SQL, `"balance"`) && !strings.Contains(st.SQL, "RETURNING") || strings.Contains(st.SQL, "to_jsonb(t0)") || strings.Contains(st.SQL, "to_jsonb(__cur)") {
			t.Errorf("hidden column reaches the hash:\n%s", st.SQL)
		}
	}
}

func TestRowPK(t *testing.T) {
	f := newFixture(t)
	users := f.res("users")
	p := &plan.Request{Method: plan.GET, Resource: users, Key: &plan.Key{Constraint: users.Table.PrimaryKey, Values: []any{1}}, Select: fields(users, "id", "email")}
	stmt := f.compile(p)
	wantSQL(t, stmt, `SELECT json_build_object('etag', md5(to_jsonb(ROW(t0."id", t0."email", t0."full_name", t0."mood", t0."settings", t0."tags", t0."balance", t0."created_at", t0."address", t0."prev_addresses", t0."moods"))::text), 'row', json_build_object('id', t0."id", 'email', t0."email")) AS data
FROM "public"."users" t0
WHERE t0."id" = $1
LIMIT 1`, 1)
	if stmt.Shape != compile.ShapeRowEtag {
		t.Errorf("shape %s", stmt.Shape)
	}
	// HEAD compiles identically.
	p.Method = plan.HEAD
	if f.compile(p).SQL != stmt.SQL {
		t.Error("HEAD differs from GET")
	}
	// Without etags: bare row.
	built := buildCatalog(t, testutil.FixtureCatalog(), false)
	users2 := built.ByPath["users"]
	p2 := &plan.Request{Method: plan.GET, Resource: users2, Key: &plan.Key{Constraint: users2.Table.PrimaryKey, Values: []any{1}}, Select: fields(users2, "id")}
	stmt2, err := compile.New(built).Compile(context.Background(), &compile.Request{Plan: p2, Built: built})
	if err != nil {
		t.Fatal(err)
	}
	if stmt2.Shape != compile.ShapeRow || !strings.HasPrefix(stmt2.SQL, `SELECT json_build_object('id', t0."id") AS data`) {
		t.Errorf("row without etag:\n%s", stmt2.SQL)
	}
}

func TestRowByUniqueWithEmbedAndFilter(t *testing.T) {
	f := newFixture(t)
	users := f.res("users")
	p := &plan.Request{Method: plan.GET, Resource: users,
		Key:    &plan.Key{Constraint: users.Uniques[0], Values: []any{"a@b.c"}},
		Select: &plan.Node{Resource: users, Fields: []plan.FieldSel{{Column: f.col("users", "id")}, {Computed: f.computed("users", "post_count")}}, Embeds: []plan.Embed{{Key: "posts", Relation: f.rel("users", "posts", ""), Node: fields(f.res("posts"), "id")}}},
		Filter: cond(f.col("users", "mood"), plan.OpEq, "happy"),
	}
	stmt := f.compile(p)
	if !strings.Contains(stmt.SQL, `WHERE t0."email" = $1 AND t0."mood" = $2`) {
		t.Errorf("unique + filter:\n%s", stmt.SQL)
	}
	if !strings.Contains(stmt.SQL, `LEFT JOIN LATERAL (`) || !strings.Contains(stmt.SQL, `'post_count', ("public"."users_post_count"(`) {
		t.Errorf("embed/computed:\n%s", stmt.SQL)
	}
	if len(stmt.Args) != 2 || stmt.Args[0] != "a@b.c" {
		t.Errorf("args: %v", stmt.Args)
	}
}

func TestRowCompositePK(t *testing.T) {
	cat := testutil.FixtureCatalog()
	cat.Tables = append(cat.Tables, &introspect.Table{
		Schema: "public", Name: "memberships", Kind: introspect.RelTable,
		Privileges: introspect.Privileges{Select: true},
		Columns: []*introspect.Column{
			{Name: "user_id", Position: 1, PGType: "int4", TypeSchema: "pg_catalog", NotNull: true},
			{Name: "team", Position: 2, PGType: "text", TypeSchema: "pg_catalog", NotNull: true},
			{Name: "role", Position: 3, PGType: "text", TypeSchema: "pg_catalog"},
		},
		PrimaryKey: &introspect.Constraint{Name: "memberships_pkey", Columns: []string{"user_id", "team"}},
	})
	built := buildCatalog(t, cat, true)
	r := built.ByPath["memberships"]
	c := compile.New(built)
	p := &plan.Request{Method: plan.GET, Resource: r, Key: &plan.Key{Constraint: r.Table.PrimaryKey, Values: []any{7, "eng"}}}
	stmt, err := c.Compile(context.Background(), &compile.Request{Plan: p, Built: built})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stmt.SQL, `WHERE t0."user_id" = $1 AND t0."team" = $2`) || len(stmt.Args) != 2 {
		t.Errorf("composite key:\n%s %v", stmt.SQL, stmt.Args)
	}
	// keyset over the composite PK
	lp := &plan.Request{Method: plan.GET, Resource: r, Page: plan.Page{Limit: 10, After: []any{7, "eng"}}, Order: []plan.OrderTerm{{Column: r.Table.Column("role")}}}
	stmt, err = c.Compile(context.Background(), &compile.Request{Plan: lp, Built: built})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stmt.SQL, `jsonb_build_array(t0."user_id", t0."team") AS __key`) ||
		!strings.Contains(stmt.SQL, `(SELECT __a."role" FROM "public"."memberships" __a WHERE __a."user_id" = $1 AND __a."team" = $2)`) ||
		!strings.Contains(stmt.SQL, `ORDER BY t0."role" ASC, t0."user_id" ASC, t0."team" ASC`) {
		t.Errorf("composite keyset:\n%s", stmt.SQL)
	}
	if !strings.Contains(stmt.SQL, `AND (t0."user_id" > $3 OR (t0."user_id" = $3 AND t0."team" > $4))`) {
		t.Errorf("composite chain:\n%s", stmt.SQL)
	}
}

func TestMultiSchemaAndAliasing(t *testing.T) {
	cat := testutil.FixtureCatalog()
	cat.Schemas = []string{"public", "audit"}
	cat.Tables = append(cat.Tables, &introspect.Table{
		Schema: "audit", Name: "log", Kind: introspect.RelTable,
		Privileges: introspect.Privileges{Select: true},
		Columns: []*introspect.Column{
			{Name: "id", Position: 1, PGType: "int8", TypeSchema: "pg_catalog", NotNull: true},
			{Name: "payload", Position: 2, PGType: "bytea", TypeSchema: "pg_catalog"},
			{Name: "amount", Position: 3, PGType: "numeric", TypeSchema: "pg_catalog"},
			{Name: "price", Position: 4, PGType: "money", TypeSchema: "pg_catalog"},
			{Name: "who", Position: 5, PGType: "int4", TypeSchema: "pg_catalog"},
		},
		PrimaryKey: &introspect.Constraint{Name: "log_pkey", Columns: []string{"id"}},
		ForeignKeys: []*introspect.ForeignKey{{
			Name: "log_who_fkey", Columns: []string{"who"}, RefSchema: "public", RefTable: "users", RefColumns: []string{"id"},
		}},
	})
	b := api.New(cat, inflect.Default, api.Options{Functions: true, BasePath: "/api", Schemas: cat.Schemas, MaxPageSize: 100, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	r := built.ByPath["audit.log"]
	if r == nil {
		t.Fatal("audit.log not built")
	}
	rel, _ := r.Resolve("users", "")
	p := &plan.Request{Method: plan.GET, Resource: r, Page: plan.Page{Limit: 10}, Select: &plan.Node{Resource: r, Star: true, Embeds: []plan.Embed{{Key: "users", Relation: rel, Node: &plan.Node{Resource: built.ByPath["users"], Fields: []plan.FieldSel{{Alias: "e", Column: built.ByPath["users"].Table.Column("email")}}}}}}}
	stmt, err := compile.New(built).Compile(context.Background(), &compile.Request{Plan: p, Built: built})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stmt.SQL, `FROM "audit"."log" t0`) || !strings.Contains(stmt.SQL, `FROM "public"."users" t1`) {
		t.Errorf("table refs:\n%s", stmt.SQL)
	}
	if !strings.Contains(stmt.SQL, `json_build_object('id', (t0."id")::text, 'payload', encode(t0."payload", 'base64'), 'amount', (t0."amount")::text, 'price', (t0."price")::text, 'who', t0."who", 'users', l2.data)`) {
		t.Errorf("type rendering:\n%s", stmt.SQL)
	}
	if !strings.Contains(stmt.SQL, `json_build_object($1::text, t1."email")`) {
		t.Errorf("alias:\n%s", stmt.SQL)
	}
}

func TestSpatialOutput(t *testing.T) {
	f := newFixture(t)
	places := f.res("places")
	p := list(places)
	loc := f.col("places", "location")
	p.Select = &plan.Node{Resource: places, Fields: []plan.FieldSel{
		{Column: f.col("places", "name")},
		{Column: loc},
		{Alias: "web", Column: loc, Args: map[string]any{"transform": 3857}},
		{Alias: "coarse", Column: f.col("places", "area"), Args: map[string]any{"simplify": 0.01, "transform": "3857"}},
		{Pseudo: &api.PseudoColumn{Key: "location_srid", Column: loc, Accessor: "srid"}},
		{Pseudo: &api.PseudoColumn{Key: "area_centroid", Column: f.col("places", "area"), Accessor: "centroid"}},
		{Alias: "gt", Pseudo: &api.PseudoColumn{Key: "location_geometry_type", Column: loc, Accessor: "geometry_type"}},
	}}
	stmt := f.compile(p)
	want := `json_build_object('name', t0."name", 'location', ST_AsGeoJSON(t0."location")::json, $1::text, ST_AsGeoJSON(ST_Transform(t0."location", 3857))::json, $3::text, ST_AsGeoJSON(ST_Transform(ST_SimplifyPreserveTopology(t0."area"::geometry, $2), 3857))::json, 'location_srid', ST_SRID(t0."location"), 'area_centroid', ST_AsGeoJSON(CASE WHEN ST_GeometryType(t0."area"::geometry) = 'ST_GeometryCollection' THEN ST_Centroid(t0."area"::geometry) ELSE ST_Centroid(t0."area")::geometry END)::json, $4::text, ST_GeometryType(t0."location"))`
	if !strings.Contains(stmt.SQL, want) {
		t.Errorf("spatial output:\nwant %s\n%s", want, stmt.SQL)
	}
	if stmt.Args[0] != "web" || stmt.Args[1] != 0.01 || stmt.Args[2] != "coarse" || stmt.Args[3] != "gt" {
		t.Errorf("args %v", stmt.Args)
	}
}

func TestChunkedJSONObject(t *testing.T) {
	cat := testutil.FixtureCatalog()
	wide := &introspect.Table{Schema: "public", Name: "wide", Kind: introspect.RelTable, Privileges: introspect.Privileges{Select: true}}
	for i := range 90 {
		wide.Columns = append(wide.Columns, &introspect.Column{Name: "c" + itoa(i), Position: i + 1, PGType: "int4", TypeSchema: "pg_catalog"})
	}
	cat.Tables = append(cat.Tables, wide)
	built := buildCatalog(t, cat, true)
	r := built.ByPath["wide"]
	stmt, err := compile.New(built).Compile(context.Background(), &compile.Request{Plan: &plan.Request{Method: plan.GET, Resource: r}, Built: built})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(stmt.SQL, "json_build_object(") != 4 || strings.Count(stmt.SQL, ` || ', ' || `) != 2 || !strings.Contains(stmt.SQL, `(left(json_build_object('caa', t0."caa"`) || !strings.Contains(stmt.SQL, `)::text, 2))::json AS data`) {
		t.Errorf("chunking:\n%s", stmt.SQL)
	}
}

func itoa(i int) string {
	return strings.TrimSpace(strings.Repeat(" ", 0) + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)))
}

func TestHooksHelpers(t *testing.T) {
	f := newFixture(t)
	ps := &compile.ParamSet{}
	if ps.Add(1) != "$1" || ps.Add("x") != "$2" || len(ps.Args()) != 2 {
		t.Error("ParamSet")
	}
	expr, err := ps.SpatialValue(f.col("places", "location"), "POINT(1 2)")
	if err != nil || expr != "CASE WHEN left($3, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($3), 4326) ELSE ST_GeomFromEWKT($3) END" || ps.Args()[2] != "SRID=4326;POINT(1 2)" {
		t.Errorf("SpatialValue: %s %v", expr, err)
	}
	if _, err := ps.SpatialValue(f.col("users", "id"), "x"); err == nil {
		t.Error("SpatialValue on non-spatial")
	}
	if compile.QuoteIdent(`a"b`) != `"a""b"` || compile.TableRefSQL("s", "t") != `"s"."t"` {
		t.Error("quoting")
	}
	if compile.CoerceInput(f.built, f.col("users", "settings"), map[string]any{"a": 1}) != `{"a":1}` {
		t.Error("CoerceInput jsonb")
	}
	if compile.CoerceInput(f.built, f.col("users", "mood"), "happy") != "happy" {
		t.Error("CoerceInput enum verbatim")
	}
	users := f.res("users")
	req := &compile.Request{Plan: list(users), Built: f.built}
	json, laterals, err := f.c.RowJSON(req, &plan.Node{Resource: users, Fields: []plan.FieldSel{{Column: f.col("users", "id")}}, Embeds: []plan.Embed{{Key: "posts", Relation: f.rel("users", "posts", ""), Node: fields(f.res("posts"), "id")}}}, "__mut", ps)
	if err != nil {
		t.Fatal(err)
	}
	if json != `json_build_object('id', __mut."id", 'posts', l1002.data)` || !strings.Contains(laterals, `WHERE t1000."author_id" = __mut."id"`) || len(ps.Args()) != 3 {
		t.Errorf("RowJSON: %s\n%s\n%v", json, laterals, ps.Args())
	}
}

func TestCost(t *testing.T) {
	f := newFixture(t)
	users := f.res("users")
	posts := f.res("posts")
	p := list(users)
	p.Page = plan.Page{Limit: 20, LimitSet: true}
	p.Select = &plan.Node{Resource: users, Fields: []plan.FieldSel{{Column: f.col("users", "id")}}, Embeds: []plan.Embed{{
		Key: "posts", Relation: f.rel("users", "posts", ""),
		Node: &plan.Node{Resource: posts, Fields: []plan.FieldSel{{Column: f.col("posts", "id")}}, Embeds: []plan.Embed{{
			Key: "comments", Relation: f.rel("posts", "comments", ""), Node: fields(f.res("comments"), "id"),
		}}},
	}}}
	if got, want := compile.Cost(p, 100), 1+20*(1+1+100*(1+1+100*1)); got != want {
		t.Errorf("cost = %d, want %d (a limitless embed fetches max_page_size rows)", got, want)
	}
	// Explicit embed limit and @costMultiplier (comments self-ref: 3).
	p.Select.Embeds[0].Page = plan.Page{Limit: 5, LimitSet: true}
	if got := compile.Cost(p, 100); got != 1+20*(1+1+5*(1+1+100*1)) {
		t.Errorf("cost = %d", got)
	}
	p.Select.Embeds[0].Page = plan.Page{Limit: 100, LimitSet: true}
	explicit := compile.Cost(p, 100)
	p.Select.Embeds[0].Page = plan.Page{}
	if limitless := compile.Cost(p, 100); limitless < explicit {
		t.Errorf("limitless embed cost %d < cost %d with limit=max_page_size", limitless, explicit)
	}
	limitless := compile.Cost(p, 100)
	p.Select.Embeds[0].Page = plan.Page{Limit: 0, LimitSet: true}
	if got := compile.Cost(p, 100); got != limitless {
		t.Errorf("limit=0 embed cost %d, want %d (the compiler applies max_page_size)", got, limitless)
	}
	p.Select.Embeds[0].Spread = true
	if got := compile.Cost(p, 100); got != limitless {
		t.Errorf("to-many spread cost %d, want %d", got, limitless)
	}
	p.Select.Embeds[0].Spread = false
	comments := f.res("comments")
	cp := list(comments)
	cp.Page = plan.Page{Limit: 10}
	cp.Select = &plan.Node{Resource: comments, Fields: []plan.FieldSel{{Column: f.col("comments", "id")}}, Embeds: []plan.Embed{{
		Key: "comments", Relation: f.rel("comments", "comments", "comments_parent_id_fkey"), Node: fields(comments, "id"),
	}}}
	if got := compile.Cost(cp, 100); got != 1+10*(1+1+3*1) {
		t.Errorf("cost multiplier = %d", got)
	}
	sp := list(posts)
	sp.Count = true
	sp.Select = &plan.Node{Resource: posts, Fields: []plan.FieldSel{{Agg: &plan.Aggregate{Func: plan.AggCount}}}, Embeds: []plan.Embed{{
		Key: "users", Spread: true, Relation: f.rel("posts", "users", ""), Node: fields(users, "mood", "email"),
	}}}
	if got := compile.Cost(sp, 100); got != 1+100*(1+1+2)+100*2 {
		t.Errorf("spread grouping cost = %d", got)
	}
	// Row: multiplier 1; nil select → all columns.
	rp := &plan.Request{Method: plan.GET, Resource: users, Key: &plan.Key{Constraint: users.Table.PrimaryKey, Values: []any{1}}}
	if got := compile.Cost(rp, 100); got != 1+len(users.Columns) {
		t.Errorf("row cost = %d", got)
	}
	recent := plan.FieldSel{Computed: f.computed("users", "recent_posts"), Node: &plan.Node{Resource: posts, Fields: []plan.FieldSel{{Column: f.col("posts", "id")}}, Embeds: []plan.Embed{{
		Key: "comments", Relation: f.rel("posts", "comments", ""), Page: plan.Page{Limit: 2, LimitSet: true}, Node: fields(f.res("comments"), "id"),
	}}}}
	rp.Select = &plan.Node{Resource: users, Fields: []plan.FieldSel{{Column: f.col("users", "id")}, recent, {Computed: f.computed("users", "tag_words")}}}
	if got, want := compile.Cost(rp, 100), 1+1+(1+100*(1+1+2*1))+(1+100); got != want {
		t.Errorf("setof computed cost = %d, want %d", got, want)
	}
	// Root list without explicit limit uses the cap.
	lp := &plan.Request{Method: plan.GET, Resource: users, Select: fields(users, "id")}
	if got := compile.Cost(lp, 100); got != 101 {
		t.Errorf("default limit cost = %d", got)
	}
}

func TestRenderGolden(t *testing.T) {
	got := compile.RenderGolden("GET", "/api/users?limit=2", &compile.Statement{SQL: "SELECT 1", Args: []any{"happy", 2}})
	want := "-- request: GET /api/users?limit=2\nSELECT 1\n-- args: [\"happy\",2]\n"
	if got != want {
		t.Errorf("golden format:\n%q\n%q", got, want)
	}
	if got := compile.RenderGolden("GET", "/x", &compile.Statement{SQL: "SELECT 1"}); !strings.HasSuffix(got, "-- args: []\n") {
		t.Errorf("empty args: %q", got)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	f := newFixture(t)
	cur := plan.EncodeCursor(f.res("posts"), []any{7})
	if cur != "WyJwdWJsaWMucG9zdHMiLDdd" {
		t.Errorf("cursor %q", cur)
	}
	name, pk, err := plan.DecodeCursor(cur)
	if err != nil || name != "public.posts" || len(pk) != 1 || pk[0].(interface{ String() string }).String() != "7" {
		t.Errorf("decode: %s %v %v", name, pk, err)
	}
	if _, _, err := plan.DecodeCursor("!!!"); err == nil {
		t.Error("garbage accepted")
	}
	if _, _, err := plan.DecodeCursor("W10"); err == nil {
		t.Error("empty array accepted")
	}
}

func TestSpatialParsedValues(t *testing.T) {
	f := newFixture(t)
	places := f.res("places")
	loc := f.col("places", "location")
	geom, err := postgis.Parse("SRID=4326;POINT(1 2)", postgis.Geometry)
	if err != nil {
		t.Fatal(err)
	}
	sp := true
	p := list(places)
	p.Select = fields(places, "id")
	p.Filter = plan.And{Terms: []plan.Expr{
		cond(loc, plan.OpStIntersects, geom),
		cond(loc, plan.OpStDWithin, plan.DWithin{Geom: geom, Distance: 5000, Spheroid: &sp}),
		cond(loc, plan.OpBBoxIntersects, plan.BBox{MinX: 1, MinY: 2, MaxX: 3, MaxY: 4, SRID: 3857}),
	}}
	p.Order = []plan.OrderTerm{{Distance: loc, Desc: true}}
	p.Near = geom
	stmt := f.compile(p)
	if !strings.Contains(stmt.SQL, `WHERE t0."location" && ST_MakeEnvelope($2, $3, $4, $5, 3857) AND ST_DWithin(t0."location"::geography, (CASE WHEN left($6, 1) = '{' THEN ST_GeomFromGeoJSON($6) ELSE ST_GeomFromEWKT($6) END)::geography, $7) AND ST_Intersects(t0."location", CASE WHEN left($8, 1) = '{' THEN ST_GeomFromGeoJSON($8) ELSE ST_GeomFromEWKT($8) END)`) {
		t.Errorf("parsed spatial values:\n%s", stmt.SQL)
	}
	if !strings.Contains(stmt.SQL, `ORDER BY t0."location" <-> CASE WHEN left($1, 1) = '{' THEN ST_GeomFromGeoJSON($1) ELSE ST_GeomFromEWKT($1) END DESC NULLS LAST, t0."id" ASC`) {
		t.Errorf("near as postgis.Value:\n%s", stmt.SQL)
	}
}

func TestComputedSetOf(t *testing.T) {
	f := newFixture(t)
	users := f.res("users")
	p := &plan.Request{Method: plan.GET, Resource: users, Key: &plan.Key{Constraint: users.Table.PrimaryKey, Values: []any{1}}}
	p.Select = &plan.Node{Resource: users, Fields: []plan.FieldSel{{Column: f.col("users", "id")}, {Computed: f.computed("users", "tag_words")}}}
	stmt := f.compile(p)
	if !strings.Contains(stmt.SQL, `'tag_words', (SELECT coalesce(json_agg(v1.v), '[]'::json) FROM "public"."users_tag_words"("u" => ROW(`) || !strings.Contains(stmt.SQL, `::"public"."users") v1(v))`) {
		t.Errorf("setof scalar computed:\n%s", stmt.SQL)
	}
}

func TestAggregateCost(t *testing.T) {
	f := newFixture(t)
	users := f.res("users")
	posts := f.res("posts")
	count := plan.FieldSel{Agg: &plan.Aggregate{Func: plan.AggCount}}
	root := list(posts)
	root.Page = plan.Page{Limit: 5, LimitSet: true}
	root.Select = &plan.Node{Resource: posts, Fields: []plan.FieldSel{{Column: f.col("posts", "author_id")}, count}}
	if got := compile.Cost(root, 100); got != 1+100*2 {
		t.Errorf("aggregated root cost = %d, want %d (limit does not bound the scan)", got, 1+100*2)
	}
	p := list(users)
	p.Page = plan.Page{Limit: 10, LimitSet: true}
	p.Select = &plan.Node{Resource: users, Fields: []plan.FieldSel{{Column: f.col("users", "id")}}, Embeds: []plan.Embed{{
		Key: "posts", Relation: f.rel("users", "posts", ""), Page: plan.Page{Limit: 2, LimitSet: true},
		Node: &plan.Node{Resource: posts, Fields: []plan.FieldSel{count}},
	}}}
	if got := compile.Cost(p, 100); got != 1+10*(1+1+100*1) {
		t.Errorf("aggregated embed cost = %d, want %d (limit does not bound the rows it reads)", got, 1+10*(1+1+100*1))
	}
	root.Count = true
	if got := compile.Cost(root, 100); got != 1+100*2+100*1 {
		t.Errorf("aggregated root with total cost = %d, want %d (the group count scans again)", got, 1+100*2+100*1)
	}
	if compile.CountPlannable(root) {
		t.Error("an aggregated list total counts groups; it is never plannable")
	}
}

func TestAggregateOrderGuard(t *testing.T) {
	f := newFixture(t)
	posts := f.res("posts")
	p := list(posts)
	p.Select = &plan.Node{Resource: posts, Fields: []plan.FieldSel{{Column: f.col("posts", "author_id")}, {Agg: &plan.Aggregate{Func: plan.AggCount}}}}
	p.Order = []plan.OrderTerm{{Column: f.col("posts", "id")}}
	_, err := f.c.Compile(context.Background(), &compile.Request{Plan: p, Built: f.built})
	var ue *compile.UserError
	if !errors.As(err, &ue) || ue.Code != compile.CodeColumnNotOrderable {
		t.Fatalf("err = %v, want column_not_orderable", err)
	}
	row := &plan.Request{Method: plan.GET, Resource: posts, Key: &plan.Key{Constraint: posts.Table.PrimaryKey, Values: []any{int64(1)}}, Select: p.Select}
	_, err = f.c.Compile(context.Background(), &compile.Request{Plan: row, Built: f.built})
	if !errors.As(err, &ue) || ue.Code != compile.CodeInvalidSelect {
		t.Fatalf("row err = %v, want invalid_select", err)
	}
	p.Order = nil
	p.Page.After = []any{int64(1)}
	_, err = f.c.Compile(context.Background(), &compile.Request{Plan: p, Built: f.built})
	if !errors.As(err, &ue) || ue.Code != compile.CodeKeysetUnsupported {
		t.Fatalf("err = %v, want keyset_unsupported", err)
	}
}

// TestCostScores pins score pseudo-columns at the cost of a column: 1 per
// row, whatever the reference.
func TestCostHeadlineAndBuffer(t *testing.T) {
	built := goldenBuilt(t)
	places := built.ByPath["places"]
	p := list(places)
	p.Page = plan.Page{Limit: 20, LimitSet: true}
	p.Select = &plan.Node{Resource: places, Fields: []plan.FieldSel{
		{Column: places.Table.Column("id")},
		{Column: places.Table.Column("area"), Args: map[string]any{"buffer": 10.0, "precision": int64(5)}},
		{Column: places.Table.Column("location"), Args: map[string]any{"precision": int64(5)}},
	}}
	if got, want := compile.Cost(p, 100), 1+20*(1+1+postgis.BufferCost+1); got != want {
		t.Errorf("buffer cost = %d, want %d", got, want)
	}
	docs := built.ByPath["kb.docs"]
	p = list(docs)
	p.Page = plan.Page{Limit: 10, LimitSet: true}
	p.Select = &plan.Node{Resource: docs, Fields: []plan.FieldSel{
		{Column: docs.Table.Column("id")},
		{Pseudo: docs.Pseudo["body_headline"], Headline: &plan.Headline{Document: docs.Table.Column("body")}},
	}}
	if got, want := compile.Cost(p, 100), 1+10*(1+contrib.HeadlineDocumentCost(contrib.MaxHeadlineDocument)); got != want {
		t.Errorf("headline cost = %d, want %d", got, want)
	}
	p.Select.Fields[1].Headline.MaxDocument = 1000
	if got, want := compile.Cost(p, 100), 1+10*(1+contrib.HeadlineCost); got != want {
		t.Errorf("headline cost over a short bound = %d, want %d", got, want)
	}
	p.Select.Fields[1].Headline.MaxDocument = 1 << 20
	if got, want := compile.Cost(p, 100), 1+10*(1+128*contrib.HeadlineCost); got != want {
		t.Errorf("headline cost over a long bound = %d, want %d", got, want)
	}
}

func TestCostScores(t *testing.T) {
	built := goldenBuilt(t)
	places := built.ByPath["places"]
	p := list(places)
	p.Page = plan.Page{Limit: 20, LimitSet: true}
	p.Select = &plan.Node{Resource: places, Fields: []plan.FieldSel{
		{Column: places.Table.Column("id")},
		{Pseudo: places.Pseudo["location_distance"], Args: map[string]any{"spheroid": true}},
		{Pseudo: places.Pseudo["area_distance"]},
	}}
	if got, want := compile.Cost(p, 100), 1+20*3; got != want {
		t.Errorf("cost = %d, want %d", got, want)
	}
}

// TestCostGeo pins the cube and earthdistance scores at the cost of a
// column, the raster metadata accessors at postgis.RasterAccessorCost, the
// pixel value at RasterValueCost and the summary statistics at
// RasterSummaryStatsCost per row; `*` over a resource with raster columns
// costs its readable columns only, and filters (the raster intersects
// filter included) are not priced.
func TestCostGeo(t *testing.T) {
	built := goldenBuilt(t)
	shapes, sites, tiles := built.ByPath["kb.shapes"], built.ByPath["kb.sites"], built.ByPath["kb.tiles"]
	p := list(shapes)
	p.Page = plan.Page{Limit: 10, LimitSet: true}
	p.Select = &plan.Node{Resource: shapes, Fields: []plan.FieldSel{{Column: shapes.Table.Column("box")}, {Pseudo: shapes.Pseudo["box_chebyshev_distance"]}}}
	if got, want := compile.Cost(p, 100), 1+10*2; got != want {
		t.Errorf("cube cost = %d, want %d", got, want)
	}
	p = list(sites)
	p.Page = plan.Page{Limit: 10, LimitSet: true}
	p.Select = &plan.Node{Resource: sites, Fields: []plan.FieldSel{{Pseudo: sites.Pseudo["loc_earth_distance"]}, {Pseudo: sites.Pseudo["ll_earth_distance_miles"]}}}
	if got, want := compile.Cost(p, 100), 1+10*2; got != want {
		t.Errorf("earth cost = %d, want %d", got, want)
	}
	p = list(tiles)
	p.Page = plan.Page{Limit: 10, LimitSet: true}
	p.Select = &plan.Node{Resource: tiles, Star: true, Fields: []plan.FieldSel{
		{Pseudo: tiles.Pseudo["rast_width"]},
		{Pseudo: tiles.Pseudo["rast_envelope"]},
		{Pseudo: tiles.Pseudo["rast_value"], Args: map[string]any{"band": int64(2)}},
		{Pseudo: tiles.Pseudo["rast_summary_stats"]},
	}}
	p.Filter = cond(tiles.Table.Column("rast"), plan.OpRasterIntersects, "POINT(1 2)")
	want := 1 + 10*(2+2*postgis.RasterAccessorCost+postgis.RasterValueCost+postgis.RasterSummaryStatsCost)
	if got := compile.Cost(p, 100); got != want {
		t.Errorf("raster cost = %d, want %d", got, want)
	}
	if postgis.RasterValueCost <= 1 || postgis.RasterSummaryStatsCost <= postgis.RasterValueCost {
		t.Errorf("raster weights %d/%d do not rank value below statistics", postgis.RasterValueCost, postgis.RasterSummaryStatsCost)
	}
}
