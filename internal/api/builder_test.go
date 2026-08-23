package api

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/inflect"
)

var update = os.Getenv("UPDATE_GOLDEN") == "1"

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func defaultOptions() Options {
	return Options{
		FilterIndexedOnly: true,
		Functions:         true,
		BasePath:          "/api",
		Schemas:           []string{"public"},
		MaxPageSize:       100,
		Etags:             true,
		Version:           "test",
		Logger:            quiet(),
	}
}

func build(t *testing.T, cat *introspect.Catalog, opts Options) *Built {
	t.Helper()
	built, err := New(cat, inflect.Default, opts).Build()
	if err != nil {
		t.Fatal(err)
	}
	return built
}

func fixture(t *testing.T) *Built { return build(t, testutil.FixtureCatalog(), defaultOptions()) }

func TestFixtureResources(t *testing.T) {
	b := fixture(t)
	var paths []string
	for p := range b.ByPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	want := []string{"comments", "events", "metrics", "places", "posts", "users"}
	if !reflect.DeepEqual(paths, want) {
		t.Errorf("resources = %v, want %v", paths, want)
	}
	users := b.ByPath["users"]
	if !users.Bare || users.Segment != "users" || !users.Keyset || !users.Etag {
		t.Errorf("users = %+v", users)
	}
	for _, m := range []Method{MethodGet, MethodHead, MethodPost, MethodPut, MethodPatch, MethodDelete} {
		if !users.Methods[m] {
			t.Errorf("users missing method %s", m)
		}
	}
	metrics := b.ByPath["metrics"]
	if metrics.Keyset || metrics.Etag {
		t.Error("PK-less view should not be keyset/etag capable")
	}
	for _, m := range []Method{MethodPost, MethodPut, MethodPatch, MethodDelete} {
		if metrics.Methods[m] {
			t.Errorf("view offers %s", m)
		}
	}
	if !metrics.Methods[MethodGet] {
		t.Error("view lost GET")
	}
	if id := users.Field("id"); id == nil || !id.ReadOnly {
		t.Error("identity column not readOnly")
	}
	if _, ok := users.InputColumns["id"]; ok {
		t.Error("generated column in InputColumns")
	}
	if _, ok := users.InputColumns["email"]; !ok {
		t.Error("email missing from InputColumns")
	}
	if len(users.Uniques) != 1 || users.Uniques[0].Name != "users_email_key" {
		t.Errorf("uniques = %+v", users.Uniques)
	}
}

func TestFixtureRoutes(t *testing.T) {
	b := fixture(t)
	got := map[string]string{}
	for _, rt := range b.Routes {
		got[rt.Pattern] = rt.AllowHeader()
	}
	want := map[string]string{
		"/users":                  "GET, HEAD, POST, PUT",
		"/users/{id}":             "GET, HEAD, PATCH, DELETE",
		"/users/by/email/{email}": "GET, HEAD, PATCH, DELETE",
		"/posts":                  "GET, HEAD, POST, PUT",
		"/posts/{id}":             "GET, HEAD, PATCH, DELETE",
		"/metrics":                "GET, HEAD",
		"/rpc/search_posts":       "GET, HEAD, POST",
		"/rpc/add_numbers":        "POST",
		"/rpc/publish_post":       "POST",
		"/rpc/authenticate":       "POST",
		"/rpc/best_mood":          "GET, HEAD, POST",
		"/rpc/retry_probe":        "POST",
		"/comments/{id}":          "GET, HEAD, PATCH, DELETE",
		"/events/{id}":            "GET, HEAD, PATCH, DELETE",
		"/places/{id}":            "GET, HEAD, PATCH, DELETE",
	}
	for p, allow := range want {
		if got[p] != allow {
			t.Errorf("route %s allow = %q, want %q", p, got[p], allow)
		}
	}
	for p := range got {
		if strings.HasPrefix(p, "/metrics/") {
			t.Errorf("PK-less view got row route %s", p)
		}
	}
	for _, name := range []string{"users_post_count", "users_recent_posts", "users_tag_words", "posts_excerpt"} {
		if _, ok := got["/rpc/"+name]; ok {
			t.Errorf("computed-column function %s exposed as RPC", name)
		}
	}
	// Deterministic ordering: resource routes sorted by path, then RPCs.
	var order []string
	for _, rt := range b.Routes {
		order = append(order, rt.Pattern)
	}
	if order[0] != "/comments" || order[len(order)-1] != "/rpc/word_lengths" {
		t.Errorf("unexpected route order: %v", order)
	}
}

func TestFixtureRelations(t *testing.T) {
	b := fixture(t)
	posts := b.ByPath["posts"]
	users := b.ByPath["users"]
	comments := b.ByPath["comments"]

	rel, cands := posts.Resolve("users", "")
	if rel == nil || cands != nil || !rel.Forward || rel.To != users || rel.FK.Name != "posts_author_id_fkey" {
		t.Errorf("posts.users = %+v (cands %v)", rel, cands)
	}
	rel, _ = users.Resolve("posts", "")
	if rel == nil || rel.Forward || rel.To != posts {
		t.Errorf("users.posts = %+v", rel)
	}
	if rel, _ := users.Resolve("places", ""); rel == nil || rel.Forward {
		t.Error("users.places backward relation missing")
	}
	// Self-reference: both directions share the key, hints required.
	rel, cands = comments.Resolve("comments", "")
	if rel != nil || len(cands) != 2 {
		t.Fatalf("comments.comments should be ambiguous: rel=%v cands=%d", rel, len(cands))
	}
	rel, _ = comments.Resolve("comments", "parent_id")
	if rel == nil || !rel.Forward {
		t.Fatalf("column hint should pick the forward (parent) side: %+v", rel)
	}
	rel, _ = comments.Resolve("comments", "comments_parent_id_fkey")
	if rel == nil || rel.Forward {
		t.Fatalf("constraint hint should pick the backward (replies) side: %+v", rel)
	}
	if rel, cands := comments.Resolve("comments", "bogus"); rel != nil || len(cands) != 2 {
		t.Error("unknown hint should report the candidates")
	}
	if rel, _ := comments.Resolve("posts", ""); rel == nil || !rel.Forward {
		t.Error("comments.posts forward relation missing")
	}
	if rel, _ := posts.Resolve("comments", ""); rel == nil || rel.Forward {
		t.Error("posts.comments backward relation missing")
	}
	if got := comments.CostMultipliers["comments"]; got != 3 {
		t.Errorf("costMultiplier = %d, want 3", got)
	}
	if rel, cands := posts.Resolve("nope", ""); rel != nil || cands != nil {
		t.Error("unknown key should resolve to nil, nil")
	}
}

func TestFixtureComputed(t *testing.T) {
	b := fixture(t)
	users := b.ByPath["users"]
	keys := sortedKeys(users.Computed)
	if !reflect.DeepEqual(keys, []string{"post_count", "recent_posts", "tag_words"}) {
		// users_recent_posts has an extra arg (n) -> skipped in v1.
		if !reflect.DeepEqual(keys, []string{"post_count", "tag_words"}) {
			t.Errorf("computed keys = %v", keys)
		}
	}
	if _, ok := users.Computed["recent_posts"]; ok {
		t.Error("computed column with extra args should be skipped in v1")
	}
	if c := users.Computed["post_count"]; c == nil || c.Function.Name != "users_post_count" || c.SetOf != nil {
		t.Errorf("post_count = %+v", c)
	}
	if c := users.Computed["tag_words"]; c == nil || !c.Function.ReturnsSet {
		t.Errorf("tag_words = %+v", c)
	}
	if _, ok := b.ByPath["posts"].Computed["excerpt"]; ok {
		t.Error("posts_excerpt has extra args; should be skipped")
	}
}

func TestComputedRealColumnWins(t *testing.T) {
	cat := testutil.FixtureCatalog()
	cat.Functions = append(cat.Functions, &introspect.Function{
		Schema: "public", Name: "users_email",
		Args:       []introspect.FuncArg{{Name: "u", PGType: "users", TypeSchema: "public"}},
		ReturnType: "text", ReturnTypeSchema: "pg_catalog", Volatility: introspect.VolatilityStable,
	})
	b := build(t, cat, defaultOptions())
	if _, ok := b.ByPath["users"].Computed["email"]; ok {
		t.Error("computed column shadowed a real column")
	}
	if !hasWarning(b.Warnings, "real column owns the key") {
		t.Errorf("warnings = %v", b.Warnings)
	}
}

func hasWarning(ws []string, sub string) bool {
	for _, w := range ws {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}

func TestFixtureRPCs(t *testing.T) {
	b := fixture(t)
	cases := map[string]struct {
		kind     ReturnKind
		mutation bool
	}{
		"search_posts":  {ReturnSetOfRows, false},
		"publish_post":  {ReturnRow, true},
		"add_numbers":   {ReturnScalar, true},
		"retry_probe":   {ReturnScalar, true},
		"authenticate":  {ReturnComposite, true},
		"best_mood":     {ReturnScalar, false},
		"all_moods":     {ReturnScalar, false},
		"mood_moods":    {ReturnSetOfScalar, false},
		"describe_mood": {ReturnScalar, false},
		"word_lengths":  {ReturnScalar, true},
	}
	for name, want := range cases {
		rpc := b.RPCByPath[name]
		if rpc == nil {
			t.Errorf("rpc %s missing", name)
			continue
		}
		if rpc.Returns != want.kind || rpc.Mutation != want.mutation {
			t.Errorf("rpc %s = %s/%v, want %s/%v", name, rpc.Returns, rpc.Mutation, want.kind, want.mutation)
		}
	}
	if rpc := b.RPCByPath["search_posts"]; rpc.Resource != b.ByPath["posts"] {
		t.Error("search_posts should return posts")
	}
	if rpc := b.RPCByPath["authenticate"]; rpc.Composite == nil || rpc.Composite.Name != "jwt" {
		t.Error("authenticate should return the jwt composite")
	}
}

func TestRPCExclusions(t *testing.T) {
	cat := testutil.FixtureCatalog()
	pg := "pg_catalog"
	cat.Functions = append(cat.Functions,
		&introspect.Function{Schema: "public", Name: "poly", Args: []introspect.FuncArg{{Name: "x", PGType: "anyelement", TypeSchema: pg}}, ReturnType: "anyelement", ReturnTypeSchema: pg, Volatility: introspect.VolatilityImmutable},
		&introspect.Function{Schema: "public", Name: "as_record", ReturnType: "record", ReturnTypeSchema: pg, ReturnsSet: true, Volatility: introspect.VolatilityStable},
		&introspect.Function{Schema: "public", Name: "handler", ReturnType: "language_handler", ReturnTypeSchema: pg, Volatility: introspect.VolatilityVolatile},
		&introspect.Function{Schema: "public", Name: "row_arg_second", Args: []introspect.FuncArg{{Name: "n", PGType: "int4", TypeSchema: pg}, {Name: "u", PGType: "users", TypeSchema: "public"}}, ReturnType: "int4", ReturnTypeSchema: pg, Volatility: introspect.VolatilityStable},
		&introspect.Function{Schema: "public", Name: "returns_hidden", ReturnType: "hidden", ReturnTypeSchema: "public", Volatility: introspect.VolatilityStable},
		&introspect.Function{Schema: "public", Name: "unnamed", Args: []introspect.FuncArg{{Name: "", PGType: "int4", TypeSchema: pg}}, ReturnType: "int4", ReturnTypeSchema: pg, Volatility: introspect.VolatilityStable},
	)
	cat.Tables = append(cat.Tables, &introspect.Table{Schema: "public", Name: "hidden", Kind: introspect.RelTable,
		Columns: []*introspect.Column{{Name: "id", Position: 1, PGType: "int4", NotNull: true}}})
	b := build(t, cat, defaultOptions())
	for _, name := range []string{"poly", "as_record", "handler", "row_arg_second", "returns_hidden", "unnamed"} {
		if _, ok := b.RPCByPath[name]; ok {
			t.Errorf("function %s should be excluded", name)
		}
	}
	if !hasWarning(b.Warnings, "not exposed") {
		t.Errorf("returns_hidden should warn: %v", b.Warnings)
	}
}

func TestRPCMint(t *testing.T) {
	opts := defaultOptions()
	opts.JWTType = "public.jwt"
	b := build(t, testutil.FixtureCatalog(), opts)
	if rpc := b.RPCByPath["authenticate"]; rpc == nil || !rpc.Mint || rpc.Returns != ReturnComposite {
		t.Fatalf("authenticate = %+v", rpc)
	}
	if b.RPCByPath["publish_post"].Mint || b.RPCByPath["best_mood"].Mint {
		t.Error("only jwt-returning functions mint")
	}
	doc := b.OpenAPI.Paths["/rpc/authenticate"]
	if doc == nil || doc.Post == nil {
		t.Fatal("authenticate path missing")
	}
	sch := doc.Post.Responses["200"].Content["application/json"].Schema
	if sch == nil || sch.Format != "jwt" {
		t.Errorf("minted response schema = %+v", sch)
	}
	if fixture(t).RPCByPath["authenticate"].Mint {
		t.Error("mint without jwt_type")
	}
}

func TestOverloadKeepFirst(t *testing.T) {
	cat := testutil.FixtureCatalog()
	cat.Functions = append(cat.Functions, &introspect.Function{
		Schema: "public", Name: "add_numbers",
		Args:       []introspect.FuncArg{{Name: "a", PGType: "int8", TypeSchema: "pg_catalog"}},
		ReturnType: "int8", ReturnTypeSchema: "pg_catalog", Volatility: introspect.VolatilityStable,
	})
	b := build(t, cat, defaultOptions())
	rpc := b.RPCByPath["add_numbers"]
	if rpc == nil || len(rpc.Args) != 2 {
		t.Fatalf("keep-first failed: %+v", rpc)
	}
	if !hasWarning(b.Warnings, "overload") {
		t.Errorf("warnings = %v", b.Warnings)
	}
}

func TestFunctionsDisabled(t *testing.T) {
	opts := defaultOptions()
	opts.Functions = false
	b := build(t, testutil.FixtureCatalog(), opts)
	if len(b.RPCs) != 0 {
		t.Error("RPCs present with functions disabled")
	}
	if len(b.ByPath["users"].Computed) != 0 {
		t.Error("computed present with functions disabled")
	}
	for _, rt := range b.Routes {
		if rt.Kind == RouteRPC {
			t.Errorf("rpc route %s present", rt.Pattern)
		}
	}
}

func TestFilterPolicy(t *testing.T) {
	b := fixture(t)
	users := b.ByPath["users"]
	for _, k := range []string{"id", "email", "mood", "tags", "settings", "moods"} {
		if !users.FilterColumns[k] || !users.OrderColumns[k] {
			t.Errorf("%s should be filterable/orderable under indexed-only", k)
		}
	}
	for _, k := range []string{"full_name", "balance", "created_at", "address", "prev_addresses"} {
		if users.FilterColumns[k] {
			t.Errorf("%s should not be filterable under indexed-only", k)
		}
	}
	if len(b.ByPath["metrics"].FilterColumns) != 0 {
		t.Error("view without indexes should have no filterable columns")
	}

	opts := defaultOptions()
	opts.AllowColumns = map[string][]string{"public.users": {"full_name"}}
	b = build(t, testutil.FixtureCatalog(), opts)
	if !b.ByPath["users"].FilterColumns["full_name"] {
		t.Error("allow_columns not applied")
	}

	opts = defaultOptions()
	opts.FilterIndexedOnly = false
	b = build(t, testutil.FixtureCatalog(), opts)
	users = b.ByPath["users"]
	if !users.FilterColumns["full_name"] || !users.FilterColumns["balance"] {
		t.Error("indexed_only=false should expose every column")
	}
	if users.FilterColumns["address"] || users.FilterColumns["prev_addresses"] {
		t.Error("composite-typed columns are never filterable")
	}
}

func TestReservedAndSchemaAliasSuppression(t *testing.T) {
	cat := testutil.FixtureCatalog()
	cat.Tables = append(cat.Tables,
		&introspect.Table{Schema: "public", Name: "rpc", Kind: introspect.RelTable,
			Privileges: introspect.Privileges{Select: true},
			Columns:    []*introspect.Column{{Name: "id", PGType: "int4", TypeSchema: "pg_catalog", NotNull: true}},
			PrimaryKey: &introspect.Constraint{Name: "rpc_pkey", Columns: []string{"id"}}},
		&introspect.Table{Schema: "public", Name: "audit", Kind: introspect.RelTable,
			Privileges: introspect.Privileges{Select: true},
			Columns:    []*introspect.Column{{Name: "id", PGType: "int4", TypeSchema: "pg_catalog", NotNull: true}}},
		&introspect.Table{Schema: "audit", Name: "users", Kind: introspect.RelTable,
			Privileges: introspect.Privileges{Select: true},
			Columns:    []*introspect.Column{{Name: "id", PGType: "int4", TypeSchema: "pg_catalog", NotNull: true}},
			PrimaryKey: &introspect.Constraint{Name: "audit_users_pkey", Columns: []string{"id"}}},
	)
	opts := defaultOptions()
	opts.Schemas = []string{"public", "audit"}
	b := build(t, cat, opts)
	if r := b.ByPath["public.rpc"]; r == nil || r.Bare {
		t.Errorf("table rpc should lose its bare alias: %+v", r)
	}
	if r := b.ByPath["public.audit"]; r == nil || r.Bare {
		t.Errorf("table audit (schema name) should lose its bare alias: %+v", r)
	}
	if r := b.ByPath["audit.users"]; r == nil || r.Bare {
		t.Errorf("audit.users should be qualified only: %+v", r)
	}
	if !hasWarning(b.Warnings, "reserved") || !hasWarning(b.Warnings, "schema name") {
		t.Errorf("warnings = %v", b.Warnings)
	}
	if m, ok := b.Match("/rpc/search_posts"); !ok || m.RPC == nil {
		t.Error("/rpc/search_posts should still be the RPC route")
	}
	if m, ok := b.Match("/public/rpc/1"); !ok || m.Route.Kind != RouteRow || m.Resource.Path != "public.rpc" {
		t.Errorf("/public/rpc/1 = %+v %v", m, ok)
	}
	if m, ok := b.Match("/audit/users/7"); !ok || m.Resource.Path != "audit.users" || m.Key != "7" {
		t.Errorf("/audit/users/7 = %+v %v", m, ok)
	}
	if m, ok := b.Match("/audit/rpc/search_posts"); ok {
		t.Errorf("no rpc in audit schema: %+v", m)
	}
	if _, ok := b.Match("/users/1"); !ok {
		t.Error("public users bare alias should survive")
	}
	if m, ok := b.Match("/public/users/1"); !ok || m.Resource.Path != "users" {
		t.Error("qualified form of a bare resource should resolve to the same resource")
	}
	if _, ok := b.Match("/audit"); ok {
		t.Error("bare schema segment alone is not a route")
	}
	if got := b.OpenAPI.Paths["/audit/users"].Get.Tags; !reflect.DeepEqual(got, []string{"audit.users"}) {
		t.Errorf("/audit/users tags = %v", got)
	}
	if got := b.OpenAPI.Paths["/public/rpc"].Get.Tags; !reflect.DeepEqual(got, []string{"public.rpc"}) {
		t.Errorf("/public/rpc tags = %v", got)
	}
}

func TestMatch(t *testing.T) {
	b := fixture(t)
	cases := []struct {
		path string
		ok   bool
		kind RouteKind
		key  string
	}{
		{"/users", true, RouteList, ""},
		{"/users/1", true, RouteRow, "1"},
		{"/users/1%2C2", true, RouteRow, "1%2C2"},
		{"/users/by/email/a%40b.c", true, RouteUnique, ""},
		{"/users/by/full_name/x", false, "", ""},
		{"/users/by/email", false, "", ""},
		{"/users/1/2", false, "", ""},
		{"/metrics", true, RouteList, ""},
		{"/metrics/1", false, "", ""},
		{"/nope", false, "", ""},
		{"/rpc/search_posts", true, RouteRPC, ""},
		{"/rpc/nope", false, "", ""},
		{"/rpc", false, "", ""},
		{"/rpc/search_posts/x", false, "", ""},
		{"/public/users", true, RouteList, ""},
		{"/public/rpc/add_numbers", true, RouteRPC, ""},
		{"/", false, "", ""},
		{"/users/%zz", false, "", ""},
	}
	for _, tc := range cases {
		m, ok := b.Match(tc.path)
		if ok != tc.ok {
			t.Errorf("Match(%q) ok = %v, want %v", tc.path, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if m.Route.Kind != tc.kind || m.Key != tc.key {
			t.Errorf("Match(%q) = %s key %q, want %s key %q", tc.path, m.Route.Kind, m.Key, tc.kind, tc.key)
		}
	}
	m, _ := b.Match("/users/by/email/a%40b.c")
	if m.Route.Unique == nil || m.Route.Unique.Name != "users_email_key" || m.Vals != "a%40b.c" || m.Cols != "email" {
		t.Errorf("unique match = %+v", m)
	}
}

func TestOpenAPIDeterministic(t *testing.T) {
	a, err := fixture(t).OpenAPI.JSON()
	if err != nil {
		t.Fatal(err)
	}
	b, err := fixture(t).OpenAPI.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Error("two builds produced different OpenAPI documents")
	}
	golden := filepath.Join("testdata", "openapi.json")
	if update {
		if err := os.WriteFile(golden, a, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with UPDATE_GOLDEN=1 to create)", err)
	}
	if !bytes.Equal(a, want) {
		t.Error("OpenAPI golden differs; review and rerun with UPDATE_GOLDEN=1")
	}
}

func TestOpenAPIShape(t *testing.T) {
	doc := fixture(t).OpenAPI
	if doc.OpenAPI != "3.1.0" || doc.Info.Title != "pdbr" || doc.Info.Version != "test" || doc.Servers[0].URL != "/api" {
		t.Errorf("top level = %+v", doc)
	}
	var tags []string
	for _, tg := range doc.Tags {
		tags = append(tags, tg.Name)
	}
	if !reflect.DeepEqual(tags, []string{"comments", "events", "metrics", "places", "posts", "users", "rpc"}) {
		t.Errorf("tags = %+v", tags)
	}
	if got := doc.Paths["/users"].Get.Tags; !reflect.DeepEqual(got, []string{"users"}) {
		t.Errorf("/users tags = %v", got)
	}
	if got := doc.Paths["/rpc/add_numbers"].Post.Tags; !reflect.DeepEqual(got, []string{"rpc"}) {
		t.Errorf("/rpc/add_numbers tags = %v", got)
	}
	s := doc.Components.Schemas
	for _, name := range []string{"users", "posts", "comments", "events", "places", "metrics", "mood", "event_type", "address", "jwt", "Problem"} {
		if s[name] == nil {
			t.Errorf("schema %s missing", name)
		}
	}
	users := s["users"]
	if got := users.Properties["balance"].Type; got != "string" {
		t.Errorf("int8 should be string, got %v", got)
	}
	if got := users.Properties["full_name"].Type; !reflect.DeepEqual(got, []string{"string", "null"}) {
		t.Errorf("nullable text = %v", got)
	}
	if !users.Properties["id"].ReadOnly {
		t.Error("identity column not readOnly")
	}
	if p := users.Properties["mood"]; len(p.AnyOf) != 2 || p.AnyOf[0].Ref != "#/components/schemas/mood" {
		t.Errorf("enum ref = %+v", p)
	}
	if p := users.Properties["post_count"]; p == nil || !p.ReadOnly {
		t.Errorf("computed property = %+v", p)
	}
	if p := users.Properties["posts"]; p == nil || p.Type != "array" || p.Items.Ref != "#/components/schemas/posts" {
		t.Errorf("backward embed = %+v", p)
	}
	if p := s["posts"].Properties["users"]; p == nil || len(p.AnyOf) != 2 {
		t.Errorf("forward embed = %+v", p)
	}
	if p := s["events"].Properties["type_code"]; p.Ref != "#/components/schemas/event_type" {
		t.Errorf("table-backed enum ref = %+v", p)
	}
	if !reflect.DeepEqual(users.Required, []string{"balance", "created_at", "email", "id"}) {
		t.Errorf("required = %v", users.Required)
	}
	if reflect.DeepEqual(s["mood"].Enum, []string{}) || len(s["mood"].Enum) != 3 {
		t.Errorf("enum values = %v", s["mood"].Enum)
	}
	if doc.Paths["/users"] == nil || doc.Paths["/users"].Get == nil || doc.Paths["/users"].Get.OperationID != "users.list" {
		t.Errorf("list path = %+v", doc.Paths["/users"])
	}
	if p := doc.Paths["/users/{id}"]; p == nil || len(p.Parameters) != 1 || p.Parameters[0].Name != "id" || p.Parameters[0].In != "path" {
		t.Errorf("row path = %+v", p)
	}
	if p := doc.Paths["/users/by/email/{email}"]; p == nil || p.Get.OperationID != "users.by_email" {
		t.Errorf("unique path = %+v", p)
	}
	if p := doc.Paths["/rpc/add_numbers"]; p == nil || p.Get != nil || p.Post == nil {
		t.Errorf("volatile rpc path = %+v", p)
	}
	if p := doc.Paths["/rpc/search_posts"]; p == nil || p.Get == nil || p.Post == nil {
		t.Errorf("stable rpc path = %+v", p)
	}
	if p := doc.Paths["/rpc/retry_probe"]; p.Post.Responses["204"] == nil {
		t.Log("retry_probe returns int4, not void")
	}
	if doc.Paths["/metrics/{id}"] != nil {
		t.Error("PK-less view has a row path")
	}
}

func TestEmptyBasePathServer(t *testing.T) {
	opts := defaultOptions()
	opts.BasePath = ""
	if b := build(t, testutil.FixtureCatalog(), opts); b.OpenAPI.Servers[0].URL != "/" {
		t.Errorf("servers = %+v", b.OpenAPI.Servers)
	}
}

func BenchmarkAPIBuild(b *testing.B) {
	cat := testutil.FixtureCatalog()
	opts := defaultOptions()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := New(cat, inflect.Default, opts).Build(); err != nil {
			b.Fatal(err)
		}
	}
}
