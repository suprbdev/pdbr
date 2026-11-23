package compile_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/plan"
)

func body(r *api.Resource, obj map[string]any) *plan.Body {
	b := &plan.Body{Object: obj}
	for _, c := range r.Table.Columns {
		if _, ok := obj[c.Name]; ok {
			b.Columns = append(b.Columns, c)
		}
	}
	return b
}

func bulk(r *api.Resource, objs ...map[string]any) *plan.Body {
	b := &plan.Body{Objects: objs}
	for _, c := range r.Table.Columns {
		for _, obj := range objs {
			if _, ok := obj[c.Name]; ok {
				b.Columns = append(b.Columns, c)
				break
			}
		}
	}
	return b
}

func pkKey(r *api.Resource, v any) *plan.Key {
	return &plan.Key{Constraint: r.Table.PrimaryKey, Values: []any{v}}
}

func has(t *testing.T, sql string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(sql, p) {
			t.Errorf("SQL lacks %q:\n%s", p, sql)
		}
	}
}

func lacks(t *testing.T, sql string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if strings.Contains(sql, p) {
			t.Errorf("SQL must not contain %q:\n%s", p, sql)
		}
	}
}

func TestMutationShapes(t *testing.T) {
	f := newFixture(t)
	users, posts := f.res("users"), f.res("posts")

	// POST single: wrapper with row + key, narrow RETURNING from select=
	st := f.compile(&plan.Request{Method: plan.POST, Resource: users, Body: body(users, map[string]any{"email": "a", "full_name": "b"}), Select: fields(users, "id", "email")})
	if st.Shape != compile.ShapeMutWrap || !st.Mutation {
		t.Errorf("shape %s mutation %v", st.Shape, st.Mutation)
	}
	has(t, st.SQL, `INSERT INTO "public"."users" ("email", "full_name")`, "VALUES ($1, $2)", `RETURNING "id", "email"`, "'key', jsonb_build_array(t0.\"id\")", "'row', json_build_object(")
	lacks(t, st.SQL, `"full_name", "mood"`)

	// PATCH plain: verbatim row, key by table-qualified column
	st = f.compile(&plan.Request{Method: plan.PATCH, Resource: users, Key: pkKey(users, int64(1)), Body: body(users, map[string]any{"full_name": "x"})})
	if st.Shape != compile.ShapeMutRow {
		t.Errorf("shape %s", st.Shape)
	}
	has(t, st.SQL, `UPDATE "public"."users"`, `SET "full_name" = $1`, `WHERE "public"."users"."id" = $2`)
	lacks(t, st.SQL, "'row'", "cur AS")

	// DELETE minimal: RETURNING 1, {n}
	st = f.compile(&plan.Request{Method: plan.DELETE, Resource: posts, Key: pkKey(posts, int64(2)), Prefer: plan.Prefer{Return: "minimal"}})
	if st.Shape != compile.ShapeMutWrap {
		t.Errorf("shape %s", st.Shape)
	}
	has(t, st.SQL, `DELETE FROM "public"."posts"`, "RETURNING 1", "'n', (SELECT count(*) FROM __mut)")

	// POST minimal keeps the pk for Location
	st = f.compile(&plan.Request{Method: plan.POST, Resource: users, Body: body(users, map[string]any{"email": "a"}), Prefer: plan.Prefer{Return: "minimal"}})
	has(t, st.SQL, `RETURNING "id"`, "'key', (SELECT jsonb_build_array(t0.\"id\") FROM __mut t0)")
	lacks(t, st.SQL, `"email"`+",")

	// bulk POST: list shape, DEFAULT gaps
	st = f.compile(&plan.Request{Method: plan.POST, Resource: users, Body: bulk(users, map[string]any{"email": "a", "full_name": "b"}, map[string]any{"email": "c"}), Select: fields(users, "id")})
	if st.Shape != compile.ShapeMutList {
		t.Errorf("shape %s", st.Shape)
	}
	has(t, st.SQL, "VALUES ($1, $2), ($3, DEFAULT)", "coalesce(json_agg(", `RETURNING "id"`)

	// {} -> DEFAULT VALUES
	st = f.compile(&plan.Request{Method: plan.POST, Resource: users, Body: &plan.Body{Object: map[string]any{}}, Select: fields(users, "id")})
	has(t, st.SQL, `INSERT INTO "public"."users" DEFAULT VALUES`)

	// upsert single: SET from EXCLUDED, __inserted, key
	pk := users.Table.PrimaryKey
	uniq := users.UniqueFor([]string{"email"})
	st = f.compile(&plan.Request{Method: plan.PUT, Resource: users, OnConflict: uniq, Body: body(users, map[string]any{"email": "a", "full_name": "b"}), Select: fields(users, "id")})
	has(t, st.SQL, `ON CONFLICT ("email") DO UPDATE`, `SET "full_name" = EXCLUDED."full_name"`, "(xmax = 0) AS __inserted", "'inserted', t0.__inserted", "'key', jsonb_build_array")
	// only-key body updates the key to itself
	st = f.compile(&plan.Request{Method: plan.PUT, Resource: users, OnConflict: uniq, Body: body(users, map[string]any{"email": "a"}), Select: fields(users, "id")})
	has(t, st.SQL, `SET "email" = EXCLUDED."email"`)
	// bulk upsert: list, no __inserted
	st = f.compile(&plan.Request{Method: plan.PUT, Resource: users, OnConflict: pk, Body: bulk(users, map[string]any{"id": int64(1), "email": "a"}), Select: fields(users, "id")})
	if st.Shape != compile.ShapeMutList {
		t.Errorf("shape %s", st.Shape)
	}
	lacks(t, st.SQL, "__inserted")

	// select= embed forces the correlation column into RETURNING
	rel := f.rel("posts", "users", "")
	st = f.compile(&plan.Request{Method: plan.POST, Resource: posts, Body: body(posts, map[string]any{"author_id": int64(1), "title": "t"}),
		Select: &plan.Node{Resource: posts, Fields: []plan.FieldSel{{Column: posts.Table.Column("id")}}, Embeds: []plan.Embed{{Key: "users", Relation: rel, Node: fields(users, "email")}}}})
	has(t, st.SQL, `RETURNING "id", "author_id"`, "LEFT JOIN LATERAL", `FROM "public"."users" t1`, `WHERE t1."id" = t0."author_id"`)

	// computed column forces every column
	st = f.compile(&plan.Request{Method: plan.POST, Resource: users, Body: body(users, map[string]any{"email": "a"}),
		Select: &plan.Node{Resource: users, Fields: []plan.FieldSel{{Computed: f.computed("users", "post_count")}}}})
	has(t, st.SQL, `RETURNING "id", "email", "full_name", "mood", "settings", "tags", "balance", "created_at", "address", "prev_addresses", "moods"`)
}

func TestMutationIfMatch(t *testing.T) {
	f := newFixture(t)
	users := f.res("users")
	req := func(ifMatch string, minimal bool) *plan.Request {
		p := &plan.Request{Method: plan.PATCH, Resource: users, Key: pkKey(users, int64(1)), Body: body(users, map[string]any{"full_name": "x"}), IfMatch: ifMatch}
		if minimal {
			p.Prefer.Return = "minimal"
		}
		return p
	}
	st := f.compile(req(`W/"abc"`, false))
	if st.Shape != compile.ShapeMutWrap {
		t.Errorf("shape %s", st.Shape)
	}
	has(t, st.SQL, "WITH cur AS (", `md5(to_jsonb(ROW(__cur."id", __cur."email", __cur."full_name", __cur."mood", __cur."settings", __cur."tags", __cur."balance", __cur."created_at", __cur."address", __cur."prev_addresses", __cur."moods"))::text) AS h`, `WHERE __cur."id" = $2`, "AND (SELECT h FROM cur) = $3", "'found', EXISTS (SELECT 1 FROM cur)", "'row', (")
	if len(st.Args) != 3 || st.Args[2] != "abc" {
		t.Errorf("args %v", st.Args)
	}
	st = f.compile(req(`"a", W/"b" , c`, false))
	has(t, st.SQL, "= ANY($3)")
	if got, ok := st.Args[2].([]string); !ok || strings.Join(got, ",") != "a,b,c" {
		t.Errorf("multi tags %#v", st.Args[2])
	}
	st = f.compile(req("*", false))
	has(t, st.SQL, "WITH cur AS (")
	lacks(t, st.SQL, "(SELECT h FROM cur) =")
	st = f.compile(req(`W/"abc"`, true))
	has(t, st.SQL, "'row', (SELECT to_json(true) FROM __mut t0)", "RETURNING 1")
	// GET-side ETag off -> If-Match ignored
	off := buildCatalog(t, f.built.Catalog, false)
	st2, err := compile.New(off).Compile(t.Context(), &compile.Request{Plan: &plan.Request{Method: plan.PATCH, Resource: off.ByPath["users"], Key: pkKey(off.ByPath["users"], int64(1)), Body: body(off.ByPath["users"], map[string]any{"full_name": "x"}), IfMatch: `W/"abc"`}, Built: off})
	if err != nil {
		t.Fatal(err)
	}
	lacks(t, st2.SQL, "cur")
}

func TestMutationInputValues(t *testing.T) {
	f := newFixture(t)
	users, places := f.res("users"), f.res("places")
	st := f.compile(&plan.Request{Method: plan.POST, Resource: users, Select: fields(users, "id"), Body: body(users, map[string]any{
		"email": "a", "settings": `{"a":1}`, "tags": []any{"x"}, "moods": []any{"ok"},
		"address": `{"street":"s"}`, "prev_addresses": `[{"street":"p"}]`,
	})})
	has(t, st.SQL,
		`("email", "settings", "tags", "address", "prev_addresses", "moods")`,
		`$2::jsonb`, `$3::"text"[]`,
		`jsonb_populate_record(NULL::"public"."address", $4::jsonb)`,
		`CASE WHEN $5::jsonb IS NULL THEN NULL ELSE coalesce((SELECT array_agg(jsonb_populate_record(NULL::"public"."address", NULLIF(e0.v, 'null'::jsonb)) ORDER BY e0.o) FROM jsonb_array_elements($5::jsonb) WITH ORDINALITY e0(v, o)), ARRAY[]::"public"."address"[]) END`,
		`$6::text[]::"public"."mood"[]`)
	// null values keep the same SQL shape
	st2 := f.compile(&plan.Request{Method: plan.POST, Resource: users, Select: fields(users, "id"), Body: body(users, map[string]any{
		"email": "a", "settings": nil, "tags": nil, "moods": nil, "address": nil, "prev_addresses": nil,
	})})
	if st.SQL != st2.SQL {
		t.Errorf("null values changed the SQL:\n%s\n%s", st.SQL, st2.SQL)
	}
	// spatial: GeoJSON vs WKT constructors, geography cast, null
	st = f.compile(&plan.Request{Method: plan.POST, Resource: places, Select: fields(places, "id"), Body: body(places, map[string]any{
		"name": "p", "location": map[string]any{"type": "Point", "coordinates": []any{1.0, 2.0}}, "area": "SRID=4326;POLYGON((0 0,1 0,1 1,0 0))",
	})})
	has(t, st.SQL, "ST_GeomFromGeoJSON($2)", "ST_GeomFromEWKT($3)", "::geography")
	st = f.compile(&plan.Request{Method: plan.POST, Resource: places, Select: fields(places, "id"), Body: body(places, map[string]any{"name": "p", "location": nil})})
	has(t, st.SQL, "$2::geometry")
	err := f.compileErr(&plan.Request{Method: plan.POST, Resource: places, Select: fields(places, "id"), Body: body(places, map[string]any{"name": "p", "location": "garbage"})})
	if !strings.Contains(err.Error(), "spatial") && !strings.Contains(err.Error(), "WKT") {
		t.Errorf("spatial error: %v", err)
	}
}

func TestMutationWithCTEs(t *testing.T) {
	f := newFixture(t)
	users, posts := f.res("users"), f.res("posts")
	rel := f.rel("users", "posts", "")
	ps := &compile.ParamSet{}
	ctes := "__mut AS (\n  INSERT INTO \"public\".\"users\" (\"email\") VALUES (" + ps.Add("a@b.c") + ") RETURNING %%RETURNING%%\n),\n" +
		"__c_1 AS (\n  INSERT INTO \"public\".\"posts\" (\"title\", \"author_id\") SELECT " + ps.Add("One") + ", __mut.\"id\" FROM __mut RETURNING *\n)"
	req := &compile.Request{Plan: &plan.Request{Method: plan.POST, Resource: users, Body: body(users, map[string]any{"email": "a@b.c"}),
		Select: &plan.Node{Resource: users, Fields: []plan.FieldSel{{Column: users.Table.Column("email")}}, Embeds: []plan.Embed{{Key: "posts", Relation: rel, Node: fields(posts, "id", "title"), Page: plan.Page{Limit: 10}}}}}, Built: f.built}
	st, err := f.c.MutationWithCTEs(req, ctes, ps, map[string][]string{"public.posts": {"__c_1"}})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Mutation || st.Shape != compile.ShapeMutWrap {
		t.Errorf("shape %s", st.Shape)
	}
	has(t, st.SQL, "WITH __mut AS (", `RETURNING "id", "email"`, "FROM __vis_1 t1", "'key', jsonb_build_array(t0.\"id\")",
		"__vis_1 AS NOT MATERIALIZED (\n  SELECT \"id\", \"author_id\", \"title\" FROM \"public\".\"posts\"\n  UNION ALL SELECT \"id\", \"author_id\", \"title\" FROM __c_1\n)")
	lacks(t, st.SQL, "%%RETURNING%%", "__vis AS", `FROM "public"."posts" t1`)
	if len(st.Args) != 3 || st.Args[0] != "a@b.c" || st.Args[1] != "One" {
		t.Errorf("args %v", st.Args)
	}
	if _, err := f.c.MutationWithCTEs(req, "", ps, nil); err == nil {
		t.Error("empty chain accepted")
	}
}

func TestMutationErrors(t *testing.T) {
	f := newFixture(t)
	users := f.res("users")
	if err := f.compileErr(&plan.Request{Method: plan.POST, Resource: users}); err == nil || strings.Contains(err.Error(), "not implemented") {
		t.Errorf("missing body: %v", err)
	}
	err := f.compileErr(&plan.Request{Method: plan.PATCH, Resource: users, Key: pkKey(users, int64(1)), Body: &plan.Body{Object: map[string]any{}}})
	var ue *compile.UserError
	if !errors.As(err, &ue) || ue.Code != compile.CodeInvalidBody {
		t.Errorf("empty patch: %v", err)
	}
	noPK := &introspect.Table{Schema: "public", Name: "nopk", Kind: introspect.RelTable, Columns: []*introspect.Column{{Name: "a", PGType: "text"}},
		Uniques: []*introspect.Constraint{{Name: "nopk_a_key", Columns: []string{"a"}}}, Privileges: introspect.Privileges{Select: true, Insert: true, Update: true, Delete: true}}
	src := f.built.Catalog
	cat := &introspect.Catalog{FormatVersion: src.FormatVersion, ServerVersion: src.ServerVersion, ServerVersionNum: src.ServerVersionNum,
		Schemas: src.Schemas, Tables: append(append([]*introspect.Table(nil), src.Tables...), noPK), Enums: src.Enums,
		Composites: src.Composites, Functions: src.Functions, Extensions: src.Extensions, Ranges: src.Ranges}
	built := buildCatalog(t, cat, true)
	r := built.ByPath["nopk"]
	if _, err := compile.New(built).Compile(t.Context(), &compile.Request{Plan: &plan.Request{Method: plan.PUT, Resource: r, Body: body(r, map[string]any{"a": "x"})}, Built: built}); err == nil {
		t.Error("PUT without target on a PK-less table compiled")
	}
	st, err := compile.New(built).Compile(t.Context(), &compile.Request{Plan: &plan.Request{Method: plan.PUT, Resource: r, OnConflict: noPK.Uniques[0], Body: body(r, map[string]any{"a": "x"})}, Built: built})
	if err != nil {
		t.Fatal(err)
	}
	has(t, st.SQL, `ON CONFLICT ("a")`, "'inserted', t0.__inserted")
	lacks(t, st.SQL, "'key'")
}

func TestBulkMutationShapes(t *testing.T) {
	f := newFixture(t)
	users := f.res("users")
	st := f.compile(&plan.Request{Method: plan.PATCH, Resource: users, Body: body(users, map[string]any{"full_name": "x"})})
	if st.Shape != compile.ShapeMutBulk || !st.Mutation || strings.Contains(st.SQL, "WHERE") || !strings.Contains(st.SQL, `UPDATE "public"."users" m0`) {
		t.Fatalf("unfiltered bulk PATCH = %s %s", st.Shape, st.SQL)
	}
	st = f.compile(&plan.Request{Method: plan.DELETE, Resource: users, Prefer: plan.Prefer{Return: "headers-only"},
		Filter: plan.Cond{Column: users.Table.Column("id"), Op: plan.OpEq, Value: int64(1)}})
	if st.Shape != compile.ShapeMutBulk || !strings.Contains(st.SQL, `WHERE m0."id" = $1`) || !strings.Contains(st.SQL, "RETURNING 1") || strings.Contains(st.SQL, "'rows'") {
		t.Fatalf("minimal bulk DELETE = %s %s", st.Shape, st.SQL)
	}
	if err := f.compileErr(&plan.Request{Method: plan.PATCH, Resource: users, Body: &plan.Body{Object: map[string]any{}}}); err == nil {
		t.Error("empty bulk PATCH compiled")
	}
	if err := f.compileErr(&plan.Request{Method: plan.DELETE, Resource: users, Filter: plan.And{}}); err == nil {
		t.Error("a filter rendering no condition compiled to an unrestricted DELETE")
	}
}
