package nestedmutations

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/exec"
	"github.com/suprbdev/pdbr/internal/inflect"
	"github.com/suprbdev/pdbr/internal/parse"
	"github.com/suprbdev/pdbr/internal/plan"
	"github.com/suprbdev/pdbr/internal/plugin"
)

func buildWith(t *testing.T, p *Plugin) *api.Built {
	t.Helper()
	b := api.New(testutil.FixtureCatalog(), inflect.Default, api.Options{
		Functions: true, BasePath: "/api", Schemas: []string{"public"}, MaxPageSize: 100, Etags: true,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if p != nil {
		if err := p.TransformAPI(context.Background(), b); err != nil {
			t.Fatal(err)
		}
	}
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	return built
}

// run parses and compiles a POST through the plugin exactly as the server
// would (parse gate from Built.Surface, plugin wrapping the core compiler).
func run(t *testing.T, p *Plugin, built *api.Built, target, body string, hdr map[string]string) (*compile.Statement, error) {
	t.Helper()
	r := httptest.NewRequest("POST", target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	m, ok := built.Match(strings.TrimPrefix(r.URL.EscapedPath(), "/api"))
	if !ok {
		t.Fatalf("no route for %s", target)
	}
	pl, err := parse.Parse(built, m, r, parse.Options{NestedKeys: built.Surface.NestedKeys})
	if err != nil {
		return nil, err
	}
	fn := p.WrapCompile(compile.New(built).Compile)
	return fn(context.Background(), &compile.Request{Plan: pl, Built: built})
}

func wantCode(t *testing.T, err error, code, detail string) {
	t.Helper()
	var ue *compile.UserError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v, want UserError %s", err, code)
	}
	if ue.Code != code || ue.Status != 400 || !strings.Contains(ue.Detail, detail) {
		t.Fatalf("err = %d %s %q, want %s containing %q", ue.Status, ue.Code, ue.Detail, code, detail)
	}
}

func TestSettings(t *testing.T) {
	p := New(nil)
	if p.Name() != "nested-mutations" || p.Priority() != 200 || !p.ForceTransactions() || p.MaxDepth() != 3 {
		t.Fatalf("defaults: %+v", p)
	}
	p = New(map[string]any{"force_transactions": false, "max_depth": 5, "bogus": 1})
	if p.ForceTransactions() || p.MaxDepth() != 5 {
		t.Fatalf("settings: %+v", p)
	}
	p = New(map[string]any{"max_depth": float64(2)})
	if p.MaxDepth() != 2 {
		t.Fatalf("float max_depth: %+v", p)
	}
	p = New(map[string]any{"max_depth": 0})
	if p.MaxDepth() != 3 {
		t.Fatalf("zero max_depth keeps default: %+v", p)
	}
	if got := strings.Join(plugin.Hooks(p), ", "); got != "api, compile, request" {
		t.Fatalf("hooks = %s", got)
	}
}

func TestNestedKeysAndSchemas(t *testing.T) {
	built := buildWith(t, New(nil))
	if built.Surface.NestedKeys == nil {
		t.Fatal("Surface.NestedKeys not set")
	}
	users := built.Resources["public.users"]
	keys := built.Surface.NestedKeys(users)
	if keys["posts"] == nil || keys["posts"].Forward || keys["places"] == nil {
		t.Fatalf("users keys = %v", keys)
	}
	posts := built.Resources["public.posts"]
	keys = built.Surface.NestedKeys(posts)
	if keys["users"] == nil || !keys["users"].Forward || keys["comments"] == nil || keys["comments"].Forward {
		t.Fatalf("posts keys = %v", keys)
	}
	// comments.parent_id -> comments: both directions share the key, so it is
	// ambiguous and skipped; the post_id relation stays.
	comments := built.Resources["public.comments"]
	keys = built.Surface.NestedKeys(comments)
	if keys["comments"] != nil || keys["posts"] == nil {
		t.Fatalf("comments keys = %v", keys)
	}
	doc, err := built.OpenAPI.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(doc, &d); err != nil {
		t.Fatal(err)
	}
	s, ok := d.Components.Schemas["users.connect"]
	if !ok {
		t.Fatalf("users.connect schema missing; have %v", keysOf(d.Components.Schemas))
	}
	if !strings.Contains(string(s), `"oneOf"`) || !strings.Contains(string(s), `"email"`) || !strings.Contains(string(s), `"id"`) {
		t.Fatalf("users.connect = %s", s)
	}
	if _, ok := d.Components.Schemas["posts.connect"]; !ok {
		t.Fatal("posts.connect schema missing")
	}
	// Disabled plugin: no seam, no schemas.
	off := buildWith(t, nil)
	if off.Surface.NestedKeys != nil {
		t.Fatal("NestedKeys set without the plugin")
	}
	doc, _ = off.OpenAPI.JSON()
	if strings.Contains(string(doc), "users.connect") {
		t.Fatal("connect schema present without the plugin")
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestValidation(t *testing.T) {
	p := New(map[string]any{"max_depth": 3})
	built := buildWith(t, p)
	cases := []struct {
		name, target, body, code, detail string
	}{
		{"forward both", "/api/posts", `{"title":"t","users":{"create":{"email":"a"},"connect":{"id":1}}}`, CodeInvalidBody, "exactly one"},
		{"forward neither", "/api/posts", `{"title":"t","users":{}}`, CodeInvalidBody, "exactly one"},
		{"forward not object", "/api/posts", `{"title":"t","users":[1]}`, CodeInvalidBody, "expected an object"},
		{"forward null", "/api/posts", `{"title":"t","users":null}`, CodeInvalidBody, "expected an object"},
		{"forward unknown wrapper key", "/api/posts", `{"title":"t","users":{"create":{"email":"a"},"x":1}}`, CodeInvalidBody, `unknown key "x"`},
		{"create not object", "/api/posts", `{"title":"t","users":{"create":[{"email":"a"}]}}`, CodeInvalidBody, `"create" must be a JSON object`},
		{"create unknown column", "/api/posts", `{"title":"t","users":{"create":{"email":"a","nope":1}}}`, CodeInvalidBody, `nested "users": unknown key "nope"`},
		{"create readonly column", "/api/posts", `{"title":"t","users":{"create":{"email":"a","id":5}}}`, parse.CodeColumnReadOnly, `nested "users"`},
		{"create missing required", "/api/posts", `{"title":"t","users":{"create":{"full_name":"a"}}}`, CodeInvalidBody, `nested "users": missing required column(s): email`},
		{"create bad value", "/api/posts", `{"title":"t","users":{"create":{"email":"a","balance":{"x":1}}}}`, CodeInvalidBody, `nested "users": "balance"`},
		{"fk and nested", "/api/posts", `{"title":"t","author_id":1,"users":{"connect":{"id":1}}}`, CodeInvalidBody, "both column"},
		{"connect not object", "/api/posts", `{"title":"t","users":{"connect":7}}`, CodeInvalidBody, `"connect" must be a JSON object`},
		{"connect empty", "/api/posts", `{"title":"t","users":{"connect":{}}}`, CodeInvalidBody, `"connect" must be a JSON object`},
		{"connect unknown key", "/api/posts", `{"title":"t","users":{"connect":{"nope":1}}}`, CodeInvalidBody, `unknown key "nope" in "connect"`},
		{"connect non-key column", "/api/posts", `{"title":"t","users":{"connect":{"full_name":"x"}}}`, CodeInvalidBody, "primary key or one unique"},
		{"connect partial key", "/api/posts", `{"title":"t","users":{"connect":{"id":1,"email":"a"}}}`, CodeInvalidBody, "primary key or one unique"},
		{"connect null", "/api/posts", `{"title":"t","users":{"connect":{"id":null}}}`, CodeInvalidBody, "must not be null"},
		{"connect bad value", "/api/posts", `{"title":"t","users":{"connect":{"id":"x"}}}`, CodeInvalidBody, `nested "users"`},
		{"backward not object", "/api/users", `{"email":"a","posts":[{"title":"t"}]}`, CodeInvalidBody, `expected an object with a "create" list`},
		{"backward connect", "/api/users", `{"email":"a","posts":{"connect":[{"id":1}]}}`, CodeInvalidBody, "not supported on a backward relation"},
		{"backward unknown key", "/api/users", `{"email":"a","posts":{"x":1}}`, CodeInvalidBody, `unknown key "x"`},
		{"backward missing create", "/api/users", `{"email":"a","posts":{}}`, CodeInvalidBody, `"create" is required`},
		{"backward create not list", "/api/users", `{"email":"a","posts":{"create":{"title":"t"}}}`, CodeInvalidBody, "non-empty JSON array"},
		{"backward create empty", "/api/users", `{"email":"a","posts":{"create":[]}}`, CodeInvalidBody, "non-empty JSON array"},
		{"backward element not object", "/api/users", `{"email":"a","posts":{"create":[{"title":"t"},1]}}`, CodeInvalidBody, `nested "posts[1]": expected a JSON object`},
		{"backward element missing required", "/api/users", `{"email":"a","posts":{"create":[{"title":"t"},{"body":"b"}]}}`, CodeInvalidBody, `nested "posts[1]": missing required column(s): title`},
		{"backward element sets parent fk", "/api/users", `{"email":"a","posts":{"create":[{"title":"t","author_id":1}]}}`, CodeInvalidBody, `nested "posts[0]": column "author_id" is filled from the parent`},
		{"backward element unknown key", "/api/users", `{"email":"a","posts":{"create":[{"title":"t","nope":1}]}}`, CodeInvalidBody, `nested "posts[0]": unknown key "nope"`},
		{"depth forward chain", "/api/comments", `{"body":"c","posts":{"create":{"title":"t","users":{"create":{"email":"a","places":{"create":[{"name":"p"}]}}}}}}`, CodeEmbedDepthExceeded, `nested "posts.users.places": nesting depth 4 exceeds max_depth 3`},
		{"depth backward chain", "/api/users", `{"email":"a","posts":{"create":[{"title":"t","comments":{"create":[{"body":"c","comments":{"create":[{"body":"d"}]}}]}}]}}`, CodeInvalidBody, `nested "posts[0].comments[0]": unknown key "comments"`},
		{"grandchild in nested", "/api/users", `{"email":"a","posts":{"create":[{"title":"t","comments":{"create":[{"body":"c","posts":{"connect":{"id":1}}}]}}]}}`, CodeInvalidBody, `nested "posts[0].comments[0]": nested relation "posts" sets column "post_id", which is filled from the parent`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := run(t, p, built, tc.target, tc.body, nil)
			if err == nil {
				t.Fatal("expected an error")
			}
			wantCode(t, err, tc.code, tc.detail)
		})
	}
}

func TestDepthBound(t *testing.T) {
	built := buildWith(t, New(nil))
	deep := `{"email":"a","posts":{"create":[{"title":"t","comments":{"create":[{"body":"c"}]}}]}}`
	if _, err := run(t, New(map[string]any{"max_depth": 3}), built, "/api/users", deep, nil); err != nil {
		t.Fatalf("depth 3 within max_depth 3: %v", err)
	}
	_, err := run(t, New(map[string]any{"max_depth": 2}), built, "/api/users", deep, nil)
	wantCode(t, err, CodeEmbedDepthExceeded, `nested "posts[0].comments": nesting depth 3 exceeds max_depth 2`)
	// A forward create counts too; connect does not add a level.
	fwd := `{"body":"c","posts":{"create":{"title":"t","users":{"create":{"email":"a"}}}}}`
	_, err = run(t, New(map[string]any{"max_depth": 2}), built, "/api/comments", fwd, nil)
	wantCode(t, err, CodeEmbedDepthExceeded, `nested "posts.users"`)
	conn := `{"body":"c","posts":{"create":{"title":"t","users":{"connect":{"id":1}}}}}`
	if _, err := run(t, New(map[string]any{"max_depth": 2}), built, "/api/comments", conn, nil); err != nil {
		t.Fatalf("connect at the bound: %v", err)
	}
}

func TestUnknownRelationPrivileges(t *testing.T) {
	p := New(nil)
	built := buildWith(t, p)
	// Drop INSERT on posts after build: create must fail with
	// unknown_relation while connect stays possible.
	built.Resources["public.posts"].Methods[api.MethodPost] = false
	_, err := run(t, p, built, "/api/users", `{"email":"a","posts":{"create":[{"title":"t"}]}}`, nil)
	wantCode(t, err, CodeUnknownRelation, `nested "posts": "posts" does not accept "create"`)
	_, err = run(t, p, built, "/api/comments", `{"body":"c","posts":{"create":{"title":"t","author_id":1}}}`, nil)
	wantCode(t, err, CodeUnknownRelation, `nested "posts"`)
	if _, err := run(t, p, built, "/api/comments", `{"body":"c","posts":{"connect":{"id":1}}}`, nil); err != nil {
		t.Fatalf("connect without INSERT: %v", err)
	}
}

func TestPluginDisabledKeyIsInvalidBody(t *testing.T) {
	built := buildWith(t, nil)
	_, err := run(t, New(nil), built, "/api/users", `{"email":"a","posts":{"create":[{"title":"t"}]}}`, nil)
	wantCode(t, err, CodeInvalidBody, `unknown key "posts"`)
}

func TestChainSQL(t *testing.T) {
	p := New(nil)
	built := buildWith(t, p)
	t.Run("children with visibility", func(t *testing.T) {
		st, err := run(t, p, built, "/api/users?select=id,email,posts(id,title)", `{"email":"a@b.c","posts":{"create":[{"title":"One"},{"title":"Two"}]}}`, nil)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{
			`__mut AS (`,
			`INSERT INTO "public"."users" ("email")`,
			`RETURNING "id", "email"`,
			`__c_1 AS (`,
			`INSERT INTO "public"."posts" ("title", "author_id")`,
			`SELECT $2, __mut."id"`,
			`FROM __mut`,
			`RETURNING *`,
			`__c_2 AS (`,
			`SELECT $3, __mut."id"`,
			"__vis_1 AS NOT MATERIALIZED (\n  SELECT \"id\", \"author_id\", \"title\" FROM \"public\".\"posts\"\n  UNION ALL SELECT \"id\", \"author_id\", \"title\" FROM __c_1\n  UNION ALL SELECT \"id\", \"author_id\", \"title\" FROM __c_2\n)",
			`FROM __vis_1 t1`,
		}
		for _, w := range want {
			if !strings.Contains(st.SQL, w) {
				t.Errorf("SQL lacks %q:\n%s", w, st.SQL)
			}
		}
		if strings.Contains(st.SQL, `FROM "public"."posts" t1`) {
			t.Errorf("embed reads the table instead of the visibility CTE:\n%s", st.SQL)
		}
		if len(st.Args) != 4 || st.Args[0] != "a@b.c" || st.Args[1] != "One" || st.Args[2] != "Two" {
			t.Errorf("args = %v", st.Args)
		}
		if !st.Mutation || st.Shape != compile.ShapeMutWrap {
			t.Errorf("stmt = %+v", st)
		}
	})
	t.Run("forward create parent", func(t *testing.T) {
		st, err := run(t, p, built, "/api/posts?select=id,title,users(id,email)", `{"title":"t","users":{"create":{"email":"a"}}}`, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range []string{
			"WITH __p_1 AS (\n  INSERT INTO \"public\".\"users\" (\"email\")\n  VALUES ($1)\n  RETURNING *\n)",
			`__mut AS (`,
			`INSERT INTO "public"."posts" ("title", "author_id")`,
			`SELECT $2, __p_1."id"`,
			`FROM __p_1`,
			"__vis_1 AS NOT MATERIALIZED (\n  SELECT \"id\", \"email\" FROM \"public\".\"users\"\n  UNION ALL SELECT \"id\", \"email\" FROM __p_1\n)",
			`FROM __vis_1 t1`,
		} {
			if !strings.Contains(st.SQL, w) {
				t.Errorf("SQL lacks %q:\n%s", w, st.SQL)
			}
		}
		if strings.Contains(st.SQL, `FROM "public"."users" t1`) {
			t.Errorf("parent embed reads the table:\n%s", st.SQL)
		}
	})
	t.Run("connect", func(t *testing.T) {
		st, err := run(t, p, built, "/api/posts", `{"title":"t","users":{"connect":{"id":1}}}`, map[string]string{"Prefer": "return=minimal"})
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range []string{
			"WITH __p_1 AS (\n  SELECT * FROM \"public\".\"users\"\n  WHERE \"id\" = $1\n)",
			`SELECT $2, __p_1."id"`,
			`RETURNING "id"`,
			`'n', (SELECT count(*) FROM __mut)`,
		} {
			if !strings.Contains(st.SQL, w) {
				t.Errorf("SQL lacks %q:\n%s", w, st.SQL)
			}
		}
		if strings.Contains(st.SQL, "__vis") {
			t.Errorf("connect must not add a visibility CTE:\n%s", st.SQL)
		}
		if len(st.Args) != 2 || st.Args[0] != int64(1) && st.Args[0] != "1" || st.Args[1] != "t" {
			t.Errorf("args = %#v", st.Args)
		}
		st2, err := run(t, p, built, "/api/posts", `{"title":"t","users":{"connect":{"email":"a@b.c"}}}`, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(st2.SQL, `WHERE "email" = $1`) {
			t.Errorf("unique connect:\n%s", st2.SQL)
		}
	})
	t.Run("nested in nested", func(t *testing.T) {
		st, err := run(t, New(map[string]any{"max_depth": 4}), built, "/api/comments?select=id,posts(id,users(id))",
			`{"body":"c","posts":{"create":{"title":"t","users":{"create":{"email":"a","places":{"create":[{"name":"p","location":{"type":"Point","coordinates":[1,2]}}]}}}}}}`, nil)
		if err != nil {
			t.Fatal(err)
		}
		order := []string{"__p_1 AS (", `INSERT INTO "public"."users"`, "__c_2 AS (", `INSERT INTO "public"."places"`, "ST_GeomFromGeoJSON($3)", `__p_1."id"`, "__p_3 AS (", `INSERT INTO "public"."posts"`, `__p_1."id"`, "__mut AS (", `__p_3."id"`, "__vis_1 AS NOT MATERIALIZED (", `UNION ALL SELECT "id" FROM __p_1`, "__vis_2 AS NOT MATERIALIZED (", `UNION ALL SELECT "id", "author_id" FROM __p_3`}
		pos := 0
		for _, w := range order {
			i := strings.Index(st.SQL[pos:], w)
			if i < 0 {
				t.Fatalf("SQL lacks %q after offset %d:\n%s", w, pos, st.SQL)
			}
			pos += i + len(w)
		}
		if len(st.Args) != 5 {
			t.Errorf("args = %#v", st.Args)
		}
		if strings.Contains(st.SQL, "FROM __c_2\n") || strings.Contains(st.SQL, "__vis_3") {
			t.Errorf("places is not read, yet has a visibility CTE:\n%s", st.SQL)
		}
	})
	t.Run("default values row", func(t *testing.T) {
		// places.owner_id is nullable; a nested users.create with nothing but
		// the FK still needs the SELECT form; the parent with only required
		// columns absent → DEFAULT VALUES is impossible on users, so use a
		// child with only the FK.
		st, err := run(t, p, built, "/api/users", `{"email":"a","places":{"create":[{"name":"x"}]}}`, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(st.SQL, `INSERT INTO "public"."places" ("name", "owner_id")`) {
			t.Errorf("SQL:\n%s", st.SQL)
		}
	})
}

// TestSQLIsShapePure asserts the SQL text does not depend on values: only
// on which keys are present and their column types.
func TestSQLIsShapePure(t *testing.T) {
	p := New(nil)
	built := buildWith(t, p)
	shape := func(body string) (string, int) {
		st, err := run(t, p, built, "/api/users?select=id,posts(id)", body, nil)
		if err != nil {
			t.Fatal(err)
		}
		return st.SQL, len(st.Args)
	}
	a, na := shape(`{"email":"a","full_name":null,"posts":{"create":[{"title":"x","body":"y"},{"title":"z"}]}}`)
	b, nb := shape(`{"email":"zzz'; DROP TABLE users; --","full_name":"Ada","posts":{"create":[{"title":"","body":null},{"title":"$1"}]}}`)
	if a != b || na != nb || na != 6 {
		t.Fatalf("SQL differs across values (%d/%d args):\n%s\n---\n%s", na, nb, a, b)
	}
	if strings.Contains(a, "DROP") || strings.Contains(a, "Ada") {
		t.Fatalf("value leaked into SQL:\n%s", a)
	}
}

func TestBeforeRequest(t *testing.T) {
	nested := &plan.Request{Body: &plan.Body{Nested: map[string]*plan.Nested{"posts": {}}}}
	plain := &plan.Request{Body: &plan.Body{Object: map[string]any{}}}
	on := New(nil)
	off := New(map[string]any{"force_transactions": false})
	for _, tc := range []struct {
		p    *Plugin
		plan *plan.Request
		want bool
	}{{on, nested, true}, {on, plain, false}, {off, nested, false}, {on, nil, false}} {
		op := &exec.Operation{Plan: tc.plan}
		if _, err := tc.p.BeforeRequest(context.Background(), op); err != nil {
			t.Fatal(err)
		}
		if op.ForceTx != tc.want {
			t.Fatalf("ForceTx = %v, want %v (%+v)", op.ForceTx, tc.want, tc)
		}
		tc.p.AfterRequest(context.Background(), op, &exec.Result{})
	}
}
