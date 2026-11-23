package api

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"
	"go.yaml.in/yaml/v3"

	"github.com/suprbdev/pdbr/internal/inflect"
)

func paramNames(ps []*Parameter, in string) []string {
	var out []string
	for _, p := range ps {
		if p.In == in {
			out = append(out, p.Name)
		}
	}
	return out
}

func paramByName(ps []*Parameter, name string) *Parameter {
	for _, p := range ps {
		if p.Name == name {
			return p
		}
	}
	return nil
}

func TestOpenAPIListParams(t *testing.T) {
	doc := fixture(t).OpenAPI
	get := doc.Paths["/users"].Get
	want := []string{"select", "order", "limit", "offset", "after", "before", "count", "distinct", "and", "or", "not.and", "not.or",
		"id", "email", "mood", "settings", "tags", "moods"}
	if got := paramNames(get.Parameters, "query"); !reflect.DeepEqual(got, want) {
		t.Errorf("users.list query params = %v, want %v", got, want)
	}
	if p := paramByName(get.Parameters, "full_name"); p != nil {
		t.Error("full_name is not indexed and must not be a filter param under indexed_only")
	}
	cases := map[string][]string{
		"id":       {"eq", "neq", "gt", "gte", "lt", "lte", "in", "is"},
		"email":    {"eq", "neq", "gt", "gte", "lt", "lte", "in", "is", "like", "ilike", "sw", "ew"},
		"settings": {"eq", "neq", "gt", "gte", "lt", "lte", "is", "cs", "cd", "hk", "jpe", "jpm"},
		"tags":     {"eq", "neq", "gt", "gte", "lt", "lte", "is", "cs", "cd", "ov"},
		"mood":     {"eq", "neq", "gt", "gte", "lt", "lte", "in", "is"},
	}
	for name, ops := range cases {
		p := paramByName(get.Parameters, name)
		if p == nil || !reflect.DeepEqual(p.XFilterOps, ops) || !strings.Contains(p.Description, strings.Join(ops, ", ")) || !strings.Contains(p.Description, "not.") {
			t.Errorf("param %s = %+v, want ops %v", name, p, ops)
		}
	}
	if p := paramByName(get.Parameters, "limit"); p.Schema.Maximum == nil || *p.Schema.Maximum != 100 || *p.Schema.Minimum != 1 {
		t.Errorf("limit schema = %+v", p.Schema)
	}
	// The PK-less view has no cursor params and no distinct-with-cursor caveat.
	if got := paramNames(doc.Paths["/metrics"].Get.Parameters, "query"); strings.Contains(strings.Join(got, ","), "after") {
		t.Errorf("metrics params = %v", got)
	}
	// Events' type_code is a table-backed enum: no pattern operators.
	if p := paramByName(doc.Paths["/events"].Get.Parameters, "type_code"); p == nil || strings.Contains(strings.Join(p.XFilterOps, ","), "like") {
		t.Errorf("type_code param = %+v", p)
	}
	// HEAD mirrors GET: same parameters, no bodies in the responses.
	head := doc.Paths["/users"].Head
	if !reflect.DeepEqual(paramNames(head.Parameters, "query"), want) || head.Responses["200"].Content != nil || head.Responses["200"].Headers["Link"] == nil {
		t.Errorf("head = %+v", head)
	}
	ok := get.Responses["200"]
	for _, h := range []string{"X-Total-Count", "X-Next-Cursor", "X-Prev-Cursor", "Link"} {
		if ok.Headers[h] == nil {
			t.Errorf("list 200 lacks header %s", h)
		}
	}
	if doc.Paths["/metrics"].Get.Responses["200"].Headers["X-Next-Cursor"] != nil {
		t.Error("offset-only resource documents cursors")
	}
	if _, has := ok.Content["text/csv"]; has {
		t.Error("text/csv documented without Options.CSV")
	}
	if paramByName(get.Parameters, "Accept") != nil {
		t.Error("Accept documented without Options.CSV")
	}
	for _, st := range []string{"400", "403", "406", "500", "503", "504", "default"} {
		if get.Responses[st] == nil {
			t.Errorf("list lacks %s response", st)
		}
	}
	if get.Responses["401"] != nil {
		t.Error("401 documented without auth")
	}
}

func TestOpenAPICSVOption(t *testing.T) {
	opts := defaultOptions()
	opts.CSV = true
	doc := build(t, testutil.FixtureCatalog(), opts).OpenAPI
	get := doc.Paths["/users"].Get
	if get.Responses["200"].Content["text/csv"] == nil || paramByName(get.Parameters, "Accept") == nil {
		t.Error("CSV surface missing")
	}
	if doc.Paths["/users/{id}"].Get.Responses["200"].Content["text/csv"] != nil {
		t.Error("single-row path documents CSV")
	}
	rpc := doc.Paths["/rpc/search_posts"]
	if rpc.Get.Responses["200"].Content["text/csv"] == nil || rpc.Post.Responses["200"].Content["text/csv"] != nil {
		t.Error("SETOF rpc: CSV on GET only")
	}
	if doc.Paths["/rpc/mood_moods"].Get.Responses["200"].Content["text/csv"] == nil {
		t.Error("SETOF scalar rpc lacks CSV")
	}
}

func TestOpenAPIInputSchemas(t *testing.T) {
	s := fixture(t).OpenAPI.Components.Schemas
	ins := s["posts.insert"]
	if ins == nil || !reflect.DeepEqual(ins.Required, []string{"author_id", "title"}) || ins.AdditionalProperties == nil || *ins.AdditionalProperties {
		t.Fatalf("posts.insert = %+v", ins)
	}
	if ins.Properties["id"] != nil {
		t.Error("identity column in insert body")
	}
	if got := sortedKeys(ins.Properties); !reflect.DeepEqual(got, []string{"author_id", "body", "published", "title"}) {
		t.Errorf("posts.insert properties = %v", got)
	}
	if u := s["users.insert"]; !reflect.DeepEqual(u.Required, []string{"email"}) || u.Properties["balance"].Type != "string" {
		t.Errorf("users.insert = %+v", u)
	}
	up := s["posts.update"]
	if up.Required != nil || up.MinProperties == nil || *up.MinProperties != 1 || up.Properties["title"].Type != "string" {
		t.Errorf("posts.update = %+v", up)
	}
	// Upsert requires the PK only when it is insertable; posts.id is
	// identity ALWAYS so the conflict target has to come from on_conflict.
	if ups := s["posts.upsert"]; !reflect.DeepEqual(ups.Required, []string{"author_id", "title"}) {
		t.Errorf("posts.upsert required = %v", ups.Required)
	}
	if s["metrics.insert"] != nil || s["metrics.update"] != nil {
		t.Error("read-only view has input schemas")
	}
	// Composite input keeps the $ref; nullable via anyOf.
	if p := s["users.insert"].Properties["address"]; len(p.AnyOf) != 2 || p.AnyOf[0].Ref != "#/components/schemas/address" {
		t.Errorf("address input = %+v", p)
	}
	// Request bodies: oneOf object|array on POST/PUT, plain object on PATCH.
	doc := fixture(t).OpenAPI
	post := doc.Paths["/posts"].Post
	if body := post.RequestBody.Content["application/json"].Schema; len(body.OneOf) != 2 || body.OneOf[0].Ref != "#/components/schemas/posts.insert" || body.OneOf[1].Items.Ref != body.OneOf[0].Ref {
		t.Errorf("post body = %+v", body)
	}
	if body := doc.Paths["/posts/{id}"].Patch.RequestBody.Content["application/json"].Schema; body.Ref != "#/components/schemas/posts.update" {
		t.Errorf("patch body = %+v", body)
	}
	if body := doc.Paths["/posts"].Put.RequestBody.Content["application/json"].Schema; body.OneOf[0].Ref != "#/components/schemas/posts.upsert" {
		t.Errorf("put body = %+v", body)
	}
	if paramByName(doc.Paths["/posts"].Put.Parameters, "on_conflict") == nil || paramByName(doc.Paths["/posts"].Post.Parameters, "Prefer") == nil {
		t.Error("PUT lacks on_conflict / POST lacks Prefer")
	}
	if post.Responses["201"].Headers["Location"] == nil || post.Responses["409"] == nil || post.Responses["405"].Ref != "#/components/responses/MethodNotAllowed" {
		t.Errorf("post responses = %+v", post.Responses)
	}
	if doc.Components.Responses["MethodNotAllowed"].Headers["Allow"] == nil {
		t.Error("MethodNotAllowed response lacks Allow header")
	}
}

func TestOpenAPIRowPaths(t *testing.T) {
	doc := fixture(t).OpenAPI
	row := doc.Paths["/users/{id}"]
	if row.Get.OperationID != "users.get" || row.Head.OperationID != "users.get.head" || row.Patch.OperationID != "users.update" || row.Delete.OperationID != "users.delete" {
		t.Errorf("row operationIds = %s %s %s %s", row.Get.OperationID, row.Head.OperationID, row.Patch.OperationID, row.Delete.OperationID)
	}
	if row.Get.Responses["200"].Headers["ETag"] == nil || row.Get.Responses["304"] == nil || paramByName(row.Get.Parameters, "If-None-Match") == nil {
		t.Error("row GET lacks ETag / 304 / If-None-Match")
	}
	if paramByName(row.Patch.Parameters, "If-Match") == nil || row.Patch.Responses["412"] == nil || row.Patch.Responses["404"] == nil || row.Delete.Responses["204"] == nil {
		t.Error("row PATCH/DELETE lack If-Match / 412 / 404 / 204")
	}
	uq := doc.Paths["/users/by/email/{email}"]
	if uq.Get.OperationID != "users.by_email" || uq.Patch.OperationID != "users.by_email.update" || uq.Delete.OperationID != "users.by_email.delete" || uq.Head.OperationID != "users.by_email.head" {
		t.Errorf("unique operationIds = %s %s %s", uq.Get.OperationID, uq.Patch.OperationID, uq.Delete.OperationID)
	}
	if uq.Parameters[0].Name != "email" || uq.Parameters[0].In != "path" || !uq.Parameters[0].Required {
		t.Errorf("unique path params = %+v", uq.Parameters[0])
	}
	// Etags off: no ETag/If-Match/304 anywhere.
	opts := defaultOptions()
	opts.Etags = false
	off := build(t, testutil.FixtureCatalog(), opts).OpenAPI.Paths["/users/{id}"]
	if off.Get.Responses["304"] != nil || paramByName(off.Patch.Parameters, "If-Match") != nil || off.Get.Responses["200"].Headers != nil {
		t.Error("etags off still documented")
	}
}

func TestOpenAPIRPCDefaultsAndVariadic(t *testing.T) {
	cat := testutil.FixtureCatalog()
	cat.Functions = append(cat.Functions, &introspect.Function{
		Schema: "public", Name: "sum_all", ReturnType: "int4", ReturnTypeSchema: "pg_catalog", Volatility: introspect.VolatilityImmutable,
		Args: []introspect.FuncArg{
			{Name: "base", PGType: "int4", TypeSchema: "pg_catalog", HasDefault: true, Default: "0"},
			{Name: "nums", PGType: "_int4", TypeSchema: "pg_catalog", Mode: introspect.ArgModeVariadic},
		},
	})
	built, err := New(cat, inflect.Default, Options{Functions: true, BasePath: "/api", Schemas: []string{"public"}, MaxPageSize: 100, Logger: quiet()}).Build()
	if err != nil {
		t.Fatal(err)
	}
	sa := built.OpenAPI.Paths["/rpc/sum_all"]
	base, nums := paramByName(sa.Get.Parameters, "base"), paramByName(sa.Get.Parameters, "nums")
	if base.Required || !strings.Contains(base.Description, "default 0") {
		t.Errorf("base = %+v", base)
	}
	if !nums.Required || nums.Schema.Type != "array" || nums.Schema.Items.Type != "integer" || nums.Explode == nil || !*nums.Explode {
		t.Errorf("nums = %+v", nums)
	}
	bs := sa.Post.RequestBody.Content["application/json"].Schema
	if !reflect.DeepEqual(bs.Required, []string{"nums"}) || !strings.Contains(bs.Properties["base"].Description, "default 0") || bs.Properties["nums"].Items == nil {
		t.Errorf("body = %+v", bs)
	}
}

func TestOpenAPIRPC(t *testing.T) {
	doc := fixture(t).OpenAPI
	sp := doc.Paths["/rpc/search_posts"]
	if got := paramNames(sp.Get.Parameters, "query"); !reflect.DeepEqual(got[:2], []string{"term", "select"}) || !strings.Contains(strings.Join(got, ","), ",author_id,title") || strings.Contains(strings.Join(got, ","), "after") {
		t.Errorf("search_posts GET params = %v", got)
	}
	if p := paramByName(sp.Get.Parameters, "term"); p.Schema.Type != "string" || !p.Required || !strings.Contains(p.Description, "required") {
		t.Errorf("term param = %+v", p)
	}
	if got := paramNames(sp.Post.Parameters, "query"); got[0] != "select" || paramByName(sp.Post.Parameters, "term") != nil {
		t.Errorf("search_posts POST params = %v", got)
	}
	body := sp.Post.RequestBody
	if bs := body.Content["application/json"].Schema; !body.Required || bs.Properties["term"] == nil || !reflect.DeepEqual(bs.Required, []string{"term"}) {
		t.Errorf("search_posts POST body = %+v", body)
	}
	if ok := sp.Get.Responses["200"]; ok.Content["application/json"].Schema.Items.Ref != "#/components/schemas/posts" || ok.Headers["X-Total-Count"] == nil || ok.Headers["Link"] == nil {
		t.Errorf("search_posts 200 = %+v", ok)
	}
	an := doc.Paths["/rpc/add_numbers"]
	if an.Get != nil || an.Post.Responses["405"] == nil || an.Post.Responses["200"].Content["application/json"].Schema.Type == nil {
		t.Errorf("add_numbers = %+v", an.Post.Responses)
	}
	if props := an.Post.RequestBody.Content["application/json"].Schema.Properties; !reflect.DeepEqual(props["a"].Type, []string{"integer", "null"}) {
		t.Errorf("add_numbers body a = %+v", props["a"])
	}
	if got := paramNames(doc.Paths["/rpc/mood_moods"].Get.Parameters, "query"); !reflect.DeepEqual(got, []string{"limit", "offset"}) {
		t.Errorf("SETOF scalar params = %v", got)
	}
	if bm := doc.Paths["/rpc/best_mood"].Get; paramNames(bm.Parameters, "query") != nil || bm.Responses["200"].Content["application/json"].Schema.AnyOf[0].Ref != "#/components/schemas/mood" {
		t.Errorf("best_mood = %+v", bm)
	}
	if dm := paramByName(doc.Paths["/rpc/describe_mood"].Get.Parameters, "m"); dm.Schema.Ref != "#/components/schemas/mood" {
		t.Errorf("enum arg = %+v", dm)
	}
	if wl := paramByName(doc.Paths["/rpc/word_lengths"].Post.Parameters, "words"); wl != nil {
		t.Error("volatile POST has query arg params")
	}
	// jwt-minting function documents a string/jwt body.
	opts := defaultOptions()
	opts.JWTType = "public.jwt"
	auth := build(t, testutil.FixtureCatalog(), opts).OpenAPI.Paths["/rpc/authenticate"].Post
	if s := auth.Responses["200"].Content["application/json"].Schema; s.Format != "jwt" {
		t.Errorf("authenticate = %+v", s)
	}
}

func TestOpenAPISecurity(t *testing.T) {
	opts := defaultOptions()
	opts.AuthMode = "jwt"
	doc := build(t, testutil.FixtureCatalog(), opts).OpenAPI
	ss := doc.Components.SecuritySchemes["bearerAuth"]
	if ss == nil || ss.Type != "http" || ss.Scheme != "bearer" || ss.BearerFormat != "JWT" {
		t.Errorf("securitySchemes = %+v", doc.Components.SecuritySchemes)
	}
	if !reflect.DeepEqual(doc.Security, []map[string][]string{{}, {"bearerAuth": {}}}) {
		t.Errorf("security = %v", doc.Security)
	}
	if doc.Paths["/users"].Get.Responses["401"] == nil {
		t.Error("401 missing in jwt mode")
	}
	opts.AuthMode = "headers"
	doc = build(t, testutil.FixtureCatalog(), opts).OpenAPI
	if doc.Components.SecuritySchemes != nil || doc.Security != nil || doc.Paths["/users"].Get.Responses["401"] == nil {
		t.Errorf("headers mode: schemes %v security %v", doc.Components.SecuritySchemes, doc.Security)
	}
	opts.AuthMode = ""
	doc = build(t, testutil.FixtureCatalog(), opts).OpenAPI
	if doc.Components.SecuritySchemes != nil || doc.Security != nil || doc.Paths["/users"].Get.Responses["401"] != nil {
		t.Error("no-auth mode documents auth")
	}
}

func TestOpenAPINestedInputs(t *testing.T) {
	b := New(testutil.FixtureCatalog(), nil, defaultOptions())
	b.Surface.NestedKeys = func(r *Resource) map[string]*Relation {
		out := map[string]*Relation{}
		for key, cands := range r.Relations {
			if len(cands) == 1 {
				out[key] = cands[0]
			}
		}
		return out
	}
	b.Extra.Schemas["users.connect"] = &Schema{Type: "object"}
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	s := built.OpenAPI.Components.Schemas
	posts := s["posts.insert"]
	users := posts.Properties["users"]
	if users == nil || users.Properties["create"].Ref != "#/components/schemas/users.insert" || users.Properties["connect"].Ref != "#/components/schemas/users.connect" ||
		users.MinProperties == nil || *users.MinProperties != 1 || *users.MaxProperties != 1 {
		t.Fatalf("posts.insert.users = %+v", users)
	}
	if !reflect.DeepEqual(posts.Required, []string{"title"}) {
		t.Errorf("FK column still required with nested key: %v", posts.Required)
	}
	back := s["users.insert"].Properties["posts"]
	if back == nil || back.Properties["create"].Type != "array" || back.Properties["create"].Items.Ref != "#/components/schemas/posts.insert" || !reflect.DeepEqual(back.Required, []string{"create"}) {
		t.Errorf("users.insert.posts = %+v", back)
	}
	// Forward key without a connect schema and whose target has POST: create only.
	if ev := s["events.insert"].Properties["event_type"]; ev != nil {
		t.Errorf("event_type is a hidden enum table; got %+v", ev)
	}
	// Update/upsert bodies never take nested input.
	if s["posts.update"].Properties["users"] != nil || s["posts.upsert"].Properties["users"] != nil {
		t.Error("nested input leaked into update/upsert")
	}
	// Without the plugin surface nothing nested renders.
	if plain := fixture(t).OpenAPI.Components.Schemas["posts.insert"]; plain.Properties["users"] != nil {
		t.Error("nested props without NestedKeys")
	}
}

func TestOpenAPISpatialSurface(t *testing.T) {
	b := New(testutil.FixtureCatalog(), nil, defaultOptions())
	b.Surface.SpatialOrder = true
	b.Surface.SpatialProjection = true
	b.Extra.FilterOps["geometry"] = []string{"st_intersects", "st_dwithin", "is"}
	b.Extra.InputTypes["geometry"] = &Schema{OneOf: []*Schema{{Type: "object"}, {Type: "string"}}}
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	get := built.OpenAPI.Paths["/places"].Get
	if p := paramByName(get.Parameters, "location"); p == nil || !reflect.DeepEqual(p.XFilterOps, []string{"st_intersects", "st_dwithin", "is"}) {
		t.Errorf("location param = %+v", p)
	}
	if p := paramByName(get.Parameters, "area"); p == nil || !reflect.DeepEqual(p.XFilterOps, []string{"is"}) {
		t.Errorf("geography without FilterOps entry = %+v", p)
	}
	if paramByName(get.Parameters, "near") == nil || !strings.Contains(paramByName(get.Parameters, "order").Description, "location.dist") {
		t.Error("near / .dist not documented")
	}
	if !strings.Contains(paramByName(get.Parameters, "select").Description, "transform=") {
		t.Error("projection args not documented")
	}
	if loc := built.OpenAPI.Components.Schemas["places.insert"].Properties["location"]; len(loc.AnyOf) != 2 || len(loc.AnyOf[0].OneOf) != 2 {
		t.Errorf("spatial input = %+v", loc)
	}
	// Without the surface: no near, spatial columns filter with is only.
	plain := fixture(t).OpenAPI.Paths["/places"].Get
	if paramByName(plain.Parameters, "near") != nil || !reflect.DeepEqual(paramByName(plain.Parameters, "location").XFilterOps, []string{"is"}) {
		t.Errorf("plain spatial params = %+v", paramByName(plain.Parameters, "location"))
	}
}

func TestOpenAPIAdvancedProse(t *testing.T) {
	b := New(testutil.FixtureCatalog(), nil, defaultOptions())
	b.Surface.RelationFilters = true
	b.Surface.ComputedFilters = true
	b.Surface.RelationOrder = true
	b.Surface.ComputedOrder = true
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	get := built.OpenAPI.Paths["/users"].Get
	if !strings.Contains(get.Description, "post_count") || !strings.Contains(get.Description, "posts") || !strings.Contains(get.Description, "exists.true") {
		t.Errorf("filter prose = %q", get.Description)
	}
	if paramByName(get.Parameters, "post_count") != nil {
		t.Error("computed filter must not be an enumerated param")
	}
	if od := paramByName(get.Parameters, "order").Description; !strings.Contains(od, "Computed: post_count") || !strings.Contains(od, "rel(col)") {
		t.Errorf("order description = %q", od)
	}
	if plain := fixture(t).OpenAPI.Paths["/users"].Get; strings.Contains(plain.Description, "exists.true") {
		t.Error("prose without the surface")
	}
	// Surface on but the relation opted out: prose omits it.
	b = New(testutil.FixtureCatalog(), nil, defaultOptions())
	b.Surface.RelationFilters = true
	for _, cands := range b.Resources["public.users"].Relations {
		for _, rel := range cands {
			rel.Filterable = false
		}
	}
	built, _ = b.Build()
	if strings.Contains(built.OpenAPI.Paths["/users"].Get.Description, "Relation filters") {
		t.Error("prose lists relations that are not filterable")
	}
}

func TestOpenAPIYAML(t *testing.T) {
	doc := fixture(t).OpenAPI
	out, err := doc.YAML()
	if err != nil {
		t.Fatal(err)
	}
	// Round trip: the YAML decodes to the same value tree as the JSON, and
	// keys come out in JSON order (top level: openapi, info, servers, ...).
	var fromYAML, fromJSON any
	if err := yaml.Unmarshal(out, &fromYAML); err != nil {
		t.Fatal(err)
	}
	js, _ := doc.JSON()
	dec := json.NewDecoder(bytes.NewReader(js))
	dec.UseNumber()
	if err := dec.Decode(&fromJSON); err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(fromYAML)
	b, _ := json.Marshal(fromJSON)
	if !bytes.Equal(a, b) {
		t.Errorf("YAML round trip differs from JSON:\n%s\n%s", a[:200], b[:200])
	}
	if !strings.HasPrefix(string(out), "openapi: 3.1.0\ninfo:\n  title: pdbr\n  version: test\nservers:\n") {
		t.Errorf("yaml head:\n%s", out[:120])
	}
	again, _ := doc.YAML()
	if !bytes.Equal(out, again) {
		t.Error("YAML rendering is not deterministic")
	}
}
