package smartcomments_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/inflect"
	"github.com/suprbdev/pdbr/internal/parse"
	"github.com/suprbdev/pdbr/internal/plugin"
	"github.com/suprbdev/pdbr/internal/plugins/smartcomments"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// build runs the whole plugin pipeline (catalog hook -> inflector -> api ->
// api hook -> Build) over the fixture catalog after edit customized it.
func build(t *testing.T, edit func(c *introspect.Catalog)) *api.Built {
	t.Helper()
	cat := testutil.FixtureCatalog()
	if edit != nil {
		edit(cat)
	}
	p := smartcomments.New(nil)
	reg := plugin.NewRegistry(p)
	ctx := context.Background()
	if err := reg.TransformCatalog(ctx, nil, cat); err != nil {
		t.Fatal(err)
	}
	b := api.New(cat, reg.Inflector(nil, inflect.Default), api.Options{
		FilterIndexedOnly: true, Functions: true, BasePath: "/api", Schemas: []string{"public"},
		MaxPageSize: 100, Etags: true, Version: "test", Logger: quiet(),
	})
	if err := reg.TransformAPI(ctx, nil, b); err != nil {
		t.Fatal(err)
	}
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	return built
}

func table(c *introspect.Catalog, name string) *introspect.Table {
	t := c.Table("public", name)
	if t == nil {
		panic("no table " + name)
	}
	return t
}

func column(c *introspect.Catalog, tbl, col string) *introspect.Column {
	x := table(c, tbl).Column(col)
	if x == nil {
		panic("no column " + tbl + "." + col)
	}
	return x
}

func fk(c *introspect.Catalog, tbl, name string) *introspect.ForeignKey {
	for _, f := range table(c, tbl).ForeignKeys {
		if f.Name == name {
			return f
		}
	}
	panic("no fk " + name)
}

func function(c *introspect.Catalog, name string) *introspect.Function {
	for _, f := range c.Functions {
		if f.Name == name {
			return f
		}
	}
	panic("no function " + name)
}

func routeMethods(b *api.Built, r *api.Resource, kind api.RouteKind) string {
	var rt *api.Route
	if kind == api.RouteUnique {
		if len(r.Uniques) > 0 {
			rt = b.UniqueRoute(r, r.Uniques[0])
		}
	} else {
		rt = b.RouteFor(r, kind)
	}
	if rt == nil {
		return "-"
	}
	return rt.AllowHeader()
}

func openapi(t *testing.T, b *api.Built) string {
	t.Helper()
	js, err := b.OpenAPI.JSON()
	if err != nil {
		t.Fatal(err)
	}
	return string(js)
}

func TestPluginIdentity(t *testing.T) {
	p := smartcomments.New(map[string]any{"ignored": true})
	if p.Name() != "smart-comments" || p.Priority() != 50 {
		t.Fatalf("name/priority = %s/%d", p.Name(), p.Priority())
	}
	if got := strings.Join(plugin.Hooks(p), ","); got != "catalog,inflection,api" {
		t.Fatalf("hooks = %s", got)
	}
}

func TestNameTable(t *testing.T) {
	b := build(t, func(c *introspect.Catalog) {
		table(c, "users").Comment = "@name people\nA person with an account."
	})
	if b.ByPath["users"] != nil || b.ByPath["people"] == nil {
		t.Fatalf("resource not renamed: %v", keys(b.ByPath))
	}
	people := b.ByPath["people"]
	if people.Description != "A person with an account." {
		t.Fatalf("description = %q", people.Description)
	}
	if _, ok := b.Match("/people/1"); !ok {
		t.Fatal("/people/1 does not route")
	}
	if _, ok := b.Match("/users"); ok {
		t.Fatal("/users still routes")
	}
	// Relation keys are table names, so the forward key follows the rename;
	// the backward key (posts on people) is unchanged.
	posts := b.ByPath["posts"]
	if posts.Relations["users"] != nil || len(posts.Relations["people"]) != 1 {
		t.Fatalf("posts relations = %v", keys(posts.Relations))
	}
	if len(people.Relations["posts"]) != 1 {
		t.Fatalf("people relations = %v", keys(people.Relations))
	}
	doc := openapi(t, b)
	if !strings.Contains(doc, `"people": {`) || strings.Contains(doc, `"users": {`) {
		t.Fatal("openapi schema not renamed")
	}
	if strings.Contains(doc, "@name") {
		t.Fatal("tag leaked into openapi")
	}
	if !strings.Contains(doc, `"description": "A person with an account."`) {
		t.Fatal("table description missing from openapi")
	}
}

func TestNameColumn(t *testing.T) {
	b := build(t, func(c *introspect.Catalog) {
		column(c, "users", "full_name").Comment = "@name display_name"
		column(c, "users", "email").Comment = "@name mail"
	})
	users := b.ByPath["users"]
	if users.Field("full_name") != nil || users.Field("display_name") == nil {
		t.Fatal("column not renamed")
	}
	if users.FilterColumns["email"] || !users.FilterColumns["mail"] || !users.OrderColumns["mail"] {
		t.Fatalf("filter/order keys not renamed: %v", keys(users.FilterColumns))
	}
	if users.InputColumns["display_name"] == nil || users.InputColumns["full_name"] != nil {
		t.Fatal("input key not renamed")
	}
	if _, ok := b.Match("/users/by/mail/x"); !ok {
		t.Fatal("/users/by/mail/x does not route")
	}
	if _, ok := b.Match("/users/by/email/x"); ok {
		t.Fatal("/users/by/email/x still routes")
	}
	rt := b.UniqueRoute(users, users.Uniques[0])
	if rt == nil || rt.Pattern != "/users/by/mail/{mail}" {
		t.Fatalf("unique route = %+v", rt)
	}
}

func TestNameFunctionAndEnum(t *testing.T) {
	b := build(t, func(c *introspect.Catalog) {
		function(c, "search_posts").Comment = "@name find_posts\nFull-text-ish search."
		function(c, "users_post_count").Comment = "@name posts_written"
		for _, e := range c.Enums {
			if e.Name == "mood" {
				e.Comment = "@name feeling"
			}
		}
	})
	if b.RPCByPath["search_posts"] != nil || b.RPCByPath["find_posts"] == nil {
		t.Fatalf("rpc not renamed: %v", keys(b.RPCByPath))
	}
	if b.RPCByPath["find_posts"].Description != "Full-text-ish search." {
		t.Fatalf("rpc description = %q", b.RPCByPath["find_posts"].Description)
	}
	if _, ok := b.Match("/rpc/find_posts"); !ok {
		t.Fatal("/rpc/find_posts does not route")
	}
	users := b.ByPath["users"]
	if users.Computed["post_count"] != nil || users.Computed["posts_written"] == nil {
		t.Fatalf("computed not renamed: %v", keys(users.Computed))
	}
	if b.Enums["public.mood"].Name != "feeling" {
		t.Fatalf("enum name = %q", b.Enums["public.mood"].Name)
	}
	doc := openapi(t, b)
	if !strings.Contains(doc, `"feeling": {`) || !strings.Contains(doc, `"$ref": "#/components/schemas/feeling"`) {
		t.Fatal("enum schema not renamed in openapi")
	}
	if !strings.Contains(doc, `"description": "Full-text-ish search."`) {
		t.Fatal("rpc description missing from openapi")
	}
}

func TestFieldNameForeignFieldName(t *testing.T) {
	b := build(t, func(c *introspect.Catalog) {
		fk(c, "posts", "posts_author_id_fkey").Comment = "@fieldName author\n@foreignFieldName articles"
	})
	posts, users := b.ByPath["posts"], b.ByPath["users"]
	if posts.Relations["users"] != nil || len(posts.Relations["author"]) != 1 || !posts.Relations["author"][0].Forward {
		t.Fatalf("forward key: %v", keys(posts.Relations))
	}
	if users.Relations["posts"] != nil || len(users.Relations["articles"]) != 1 || users.Relations["articles"][0].Forward {
		t.Fatalf("backward key: %v", keys(users.Relations))
	}
	// Other FKs to users are untouched.
	if len(users.Relations["places"]) != 1 {
		t.Fatalf("places relation lost: %v", keys(users.Relations))
	}
}

func TestOmitTable(t *testing.T) {
	b := build(t, func(c *introspect.Catalog) {
		table(c, "comments").Comment = "@omit"
	})
	if b.ByPath["comments"] != nil {
		t.Fatal("comments still exposed")
	}
	if len(b.ByPath["posts"].Relations["comments"]) != 0 {
		t.Fatal("backward relation to omitted table survives")
	}
	if strings.Contains(openapi(t, b), `"comments"`) {
		t.Fatal("comments in openapi")
	}
}

func TestOmitTableActions(t *testing.T) {
	cases := []struct {
		tag                   string
		list, row, unique     string
		filterable, orderable bool
	}{
		{"@omit read", "POST, PUT", "PATCH, DELETE", "PATCH, DELETE", true, true},
		{"@omit all", "POST, PUT", "GET, HEAD, PATCH, DELETE", "GET, HEAD, PATCH, DELETE", true, true},
		{"@omit create", "GET, HEAD, PATCH, DELETE", "GET, HEAD, PATCH, DELETE", "GET, HEAD, PATCH, DELETE", true, true},
		{"@omit update", "GET, HEAD, POST, DELETE", "GET, HEAD, DELETE", "GET, HEAD, DELETE", true, true},
		{"@omit delete", "GET, HEAD, POST, PUT, PATCH", "GET, HEAD, PATCH", "GET, HEAD, PATCH", true, true},
		{"@omit create, delete", "GET, HEAD, PATCH", "GET, HEAD, PATCH", "GET, HEAD, PATCH", true, true},
		{"@omit read, update, delete", "POST", "-", "-", true, true},
		{"@behavior -insert -delete", "GET, HEAD, PATCH", "GET, HEAD, PATCH", "GET, HEAD, PATCH", true, true},
		{"@omit many", "GET, HEAD, POST, PUT, PATCH, DELETE", "GET, HEAD, PATCH, DELETE", "GET, HEAD, PATCH, DELETE", true, true},
		{"@omit filter", "GET, HEAD, POST, PUT, PATCH, DELETE", "GET, HEAD, PATCH, DELETE", "GET, HEAD, PATCH, DELETE", false, true},
		{"@omit order", "GET, HEAD, POST, PUT, PATCH, DELETE", "GET, HEAD, PATCH, DELETE", "GET, HEAD, PATCH, DELETE", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.tag, func(t *testing.T) {
			b := build(t, func(c *introspect.Catalog) {
				table(c, "users").Comment = tc.tag
			})
			users := b.ByPath["users"]
			if users == nil {
				t.Fatal("users vanished")
			}
			if got := routeMethods(b, users, api.RouteList); got != tc.list {
				t.Errorf("list methods = %q, want %q", got, tc.list)
			}
			if got := routeMethods(b, users, api.RouteRow); got != tc.row {
				t.Errorf("row methods = %q, want %q", got, tc.row)
			}
			if got := routeMethods(b, users, api.RouteUnique); got != tc.unique {
				t.Errorf("unique methods = %q, want %q", got, tc.unique)
			}
			// users is never the many side of an FK in the fixture, so @omit
			// many on it removes nothing; both embeds must survive.
			posts := b.ByPath["posts"]
			if len(posts.Relations["users"]) != 1 {
				t.Errorf("forward embed posts->users lost")
			}
			if len(users.Relations["posts"]) != 1 {
				t.Errorf("users->posts backward embed lost")
			}
			if (len(users.FilterColumns) > 0) != tc.filterable {
				t.Errorf("filterable = %v, want %v", len(users.FilterColumns) > 0, tc.filterable)
			}
			if (len(users.OrderColumns) > 0) != tc.orderable {
				t.Errorf("orderable = %v, want %v", len(users.OrderColumns) > 0, tc.orderable)
			}
			// Still embeddable and still in components.schemas.
			if !strings.Contains(openapi(t, b), `"users": {`) {
				t.Errorf("users schema missing from openapi")
			}
		})
	}
}

func TestOmitManyTable(t *testing.T) {
	// posts is the many side of users -> posts and the one side of comments.
	b := build(t, func(c *introspect.Catalog) {
		table(c, "posts").Comment = "@omit many"
	})
	users, posts, comments := b.ByPath["users"], b.ByPath["posts"], b.ByPath["comments"]
	if users.Relations["posts"] != nil {
		t.Fatal("users?select=posts(*) survives @omit many")
	}
	if len(comments.Relations["posts"]) != 1 || len(posts.Relations["users"]) != 1 {
		t.Fatal("forward embeds lost")
	}
	if len(posts.Relations["comments"]) != 1 {
		t.Fatal("posts' own backward embed lost")
	}
	if b.OpenAPI.Components.Schemas["users"].Properties["posts"] != nil {
		t.Fatal("openapi still documents users.posts embed")
	}
	if b.OpenAPI.Components.Schemas["comments"].Properties["posts"] == nil {
		t.Fatal("openapi lost comments.posts forward embed")
	}
}

func TestOmitColumn(t *testing.T) {
	b := build(t, func(c *introspect.Catalog) {
		column(c, "users", "settings").Comment = "@omit"
		column(c, "users", "email").Comment = "@omit read"
		column(c, "users", "full_name").Comment = "@omit create"
		column(c, "users", "mood").Comment = "@omit update"
		column(c, "users", "tags").Comment = "@omit filter"
		column(c, "users", "id").Comment = "@omit order"
		column(c, "users", "balance").Comment = "@behavior -select"
	})
	users := b.ByPath["users"]
	if users.Field("settings") != nil || users.FilterColumns["settings"] || users.OrderColumns["settings"] || users.InputColumns["settings"] != nil {
		t.Fatal("@omit column still exposed somewhere")
	}
	if users.Field("email") != nil || users.Field("balance") != nil {
		t.Fatal("@omit read column still in responses")
	}
	if users.InputColumns["email"] == nil {
		t.Fatal("@omit read column dropped from bodies")
	}
	if len(users.Uniques) != 0 || routeMethods(b, users, api.RouteUnique) != "-" {
		t.Fatal("unique route over a hidden column survives")
	}
	if users.FilterColumns["tags"] || !users.OrderColumns["tags"] {
		t.Fatal("@omit filter wrong")
	}
	if users.OrderColumns["id"] || !users.FilterColumns["id"] {
		t.Fatal("@omit order wrong")
	}
	post := users.InputColumnsFor(api.MethodPost)
	patch := users.InputColumnsFor(api.MethodPatch)
	put := users.InputColumnsFor(api.MethodPut)
	if post["full_name"] != nil || patch["full_name"] == nil || put["full_name"] != nil {
		t.Fatal("@omit create wrong")
	}
	if patch["mood"] != nil || post["mood"] == nil || put["mood"] != nil {
		t.Fatal("@omit update wrong")
	}
	if len(users.InputColumnsFor(api.MethodGet)) != len(users.InputColumns) {
		t.Fatal("GET input columns")
	}
	doc := openapi(t, b)
	for _, absent := range []string{`"settings"`, `"email"`, `"balance"`} {
		if strings.Contains(doc, absent) {
			t.Errorf("%s still in openapi", absent)
		}
	}
	// select= and body parsing honour the removals end to end.
	req := httptest.NewRequest(http.MethodGet, "/api/users?select=id,email", nil)
	m, _ := b.Match("/users")
	if _, err := parse.Parse(b, m, req, parse.Options{}); err == nil || !strings.Contains(err.Error(), "unknown column") {
		t.Fatalf("select=email err = %v", err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/users", strings.NewReader(`{"email":"x@y","full_name":"n"}`))
	req.Header.Set("Content-Type", "application/json")
	_, err := parse.Parse(b, m, req, parse.Options{})
	if err == nil || !strings.Contains(err.Error(), `column "full_name" of "users" cannot be written by POST`) {
		t.Fatalf("POST full_name err = %v", err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/users", strings.NewReader(`{"email":"x@y","mood":"ok"}`))
	req.Header.Set("Content-Type", "application/json")
	if _, err := parse.Parse(b, m, req, parse.Options{}); err != nil {
		t.Fatalf("POST mood err = %v", err)
	}
	req = httptest.NewRequest(http.MethodPatch, "/api/users/1", strings.NewReader(`{"mood":"ok"}`))
	req.Header.Set("Content-Type", "application/json")
	m, _ = b.Match("/users/1")
	if _, err := parse.Parse(b, m, req, parse.Options{}); err == nil || !strings.Contains(err.Error(), "cannot be written by PATCH") {
		t.Fatalf("PATCH mood err = %v", err)
	}
}

func TestOmitConstraint(t *testing.T) {
	b := build(t, func(c *introspect.Catalog) {
		fk(c, "posts", "posts_author_id_fkey").Comment = "@omit"
		fk(c, "comments", "comments_post_id_fkey").Comment = "@omit many"
		fk(c, "places", "places_owner_id_fkey").Comment = "@omit filter, order"
		table(c, "users").Uniques[0].Comment = "@omit"
	})
	users, posts, comments, places := b.ByPath["users"], b.ByPath["posts"], b.ByPath["comments"], b.ByPath["places"]
	if posts.Relations["users"] != nil || users.Relations["posts"] != nil {
		t.Fatal("@omit fk left a relation")
	}
	if posts.Relations["comments"] != nil || len(comments.Relations["posts"]) != 1 {
		t.Fatal("@omit many fk wrong")
	}
	fwd, bwd := places.Relations["users"][0], users.Relations["places"][0]
	if fwd.Filterable || fwd.Orderable || bwd.Filterable || bwd.Orderable || !fwd.Embeddable || !bwd.Embeddable {
		t.Fatalf("@omit filter,order fk: fwd=%+v bwd=%+v", fwd, bwd)
	}
	if len(users.Uniques) != 0 || routeMethods(b, users, api.RouteUnique) != "-" {
		t.Fatal("@omit unique left the /by/ route")
	}
	if !users.FilterColumns["email"] {
		t.Fatal("@omit unique removed filterability")
	}
	if _, ok := b.Match("/users/by/email/x"); ok {
		t.Fatal("/users/by/email still routes")
	}
}

func TestOmitFunction(t *testing.T) {
	b := build(t, func(c *introspect.Catalog) {
		function(c, "search_posts").Comment = "@omit"
		function(c, "users_post_count").Comment = "@omit"
	})
	if b.RPCByPath["search_posts"] != nil {
		t.Fatal("rpc survives @omit")
	}
	if b.ByPath["users"].Computed["post_count"] != nil {
		t.Fatal("computed survives @omit")
	}
}

func TestOmitFilterOrderComputed(t *testing.T) {
	b := build(t, func(c *introspect.Catalog) {
		function(c, "users_post_count").Comment = "@omit filter"
		table(c, "posts").Comment = "@omit filter, order"
	})
	pc := b.ByPath["users"].Computed["post_count"]
	if pc.Filterable || !pc.Orderable {
		t.Fatalf("post_count filterable=%v orderable=%v", pc.Filterable, pc.Orderable)
	}
	posts := b.ByPath["posts"]
	if len(posts.FilterColumns) != 0 || len(posts.OrderColumns) != 0 {
		t.Fatal("table @omit filter, order left columns")
	}
	for k, c := range posts.Computed {
		if c.Filterable || c.Orderable {
			t.Fatalf("computed %s still filterable/orderable", k)
		}
	}
}

func TestDeprecated(t *testing.T) {
	b := build(t, func(c *introspect.Catalog) {
		table(c, "posts").Comment = "@deprecated use articles"
		column(c, "users", "balance").Comment = "@deprecated use credits\nAccount balance."
		function(c, "search_posts").Comment = "@deprecated"
		function(c, "users_post_count").Comment = "@deprecated count posts yourself"
	})
	posts, users := b.ByPath["posts"], b.ByPath["users"]
	if !posts.Deprecated || posts.DeprecationReason != "use articles" {
		t.Fatalf("posts = %v %q", posts.Deprecated, posts.DeprecationReason)
	}
	f := users.Field("balance")
	if !f.Deprecated || f.DeprecationReason != "use credits" || f.Description != "Account balance." {
		t.Fatalf("balance = %+v", f)
	}
	rpc := b.RPCByPath["search_posts"]
	if !rpc.Deprecated || rpc.DeprecationReason != "No longer supported" {
		t.Fatalf("rpc = %v %q", rpc.Deprecated, rpc.DeprecationReason)
	}
	c := users.Computed["post_count"]
	if !c.Deprecated || c.DeprecationReason != "count posts yourself" {
		t.Fatalf("computed = %v %q", c.Deprecated, c.DeprecationReason)
	}
	doc := openapi(t, b)
	for _, want := range []string{
		`"deprecated": true`,
		`"x-deprecation-reason": "use articles"`,
		`"x-deprecation-reason": "use credits"`,
		`"x-deprecation-reason": "No longer supported"`,
		`"x-deprecation-reason": "count posts yourself"`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("openapi lacks %s", want)
		}
	}
	// Every operation on the deprecated resource's paths carries the flag.
	for _, path := range []string{"/posts", "/posts/{id}"} {
		item := b.OpenAPI.Paths[path]
		if item == nil || item.Get == nil || !item.Get.Deprecated || item.Get.XDeprecationReason != "use articles" {
			t.Errorf("%s get not deprecated", path)
		}
	}
	if !b.OpenAPI.Paths["/rpc/search_posts"].Get.Deprecated {
		t.Error("rpc operation not deprecated")
	}
}

func TestNotNullNullable(t *testing.T) {
	b := build(t, func(c *introspect.Catalog) {
		column(c, "metrics", "name").Comment = "@notNull"
		column(c, "users", "email").Comment = "@nullable"
	})
	if !b.ByPath["metrics"].Field("name").Column.NotNull || b.ByPath["users"].Field("email").Column.NotNull {
		t.Fatal("nullability not applied")
	}
	doc := b.OpenAPI.Components.Schemas
	if doc["metrics"].Properties["name"].Type != "string" {
		t.Fatalf("metrics.name type = %v", doc["metrics"].Properties["name"].Type)
	}
	if strings.Contains(strings.Join(doc["users"].Required, ","), "email") {
		t.Fatal("email still required")
	}
}

func TestFilterableSortable(t *testing.T) {
	// posts.published has no index: not filterable under indexed_only.
	base := build(t, nil)
	if base.ByPath["posts"].FilterColumns["published"] {
		t.Fatal("precondition: published filterable without tag")
	}
	for _, tag := range []string{"@filterable", "@sortable"} {
		b := build(t, func(c *introspect.Catalog) {
			column(c, "posts", "published").Comment = tag
			column(c, "posts", "body").Comment = tag + "\n@omit order"
		})
		posts := b.ByPath["posts"]
		if !posts.FilterColumns["published"] || !posts.OrderColumns["published"] {
			t.Fatalf("%s: published filter=%v order=%v", tag, posts.FilterColumns["published"], posts.OrderColumns["published"])
		}
		if !posts.FilterColumns["body"] || posts.OrderColumns["body"] {
			t.Fatalf("%s + @omit order: body filter=%v order=%v", tag, posts.FilterColumns["body"], posts.OrderColumns["body"])
		}
	}
}

func TestViewKeys(t *testing.T) {
	b := build(t, func(c *introspect.Catalog) {
		table(c, "metrics").Comment = "@primaryKey name\n@unique value\n@foreignKey (value) references users (id)|@fieldName owner|@foreignFieldName stats"
	})
	metrics, users := b.ByPath["metrics"], b.ByPath["users"]
	if !metrics.Keyset || !metrics.Etag {
		t.Fatalf("keyset=%v etag=%v", metrics.Keyset, metrics.Etag)
	}
	if got := routeMethods(b, metrics, api.RouteRow); got != "GET, HEAD" {
		t.Fatalf("row methods = %q (views stay read-only)", got)
	}
	if rt := b.RouteFor(metrics, api.RouteRow); rt.Pattern != "/metrics/{name}" {
		t.Fatalf("row pattern = %s", rt.Pattern)
	}
	if rt := b.UniqueRoute(metrics, metrics.Uniques[0]); len(metrics.Uniques) != 1 || rt == nil || rt.Pattern != "/metrics/by/value/{value}" {
		t.Fatalf("unique route = %+v", rt)
	}
	if len(metrics.Relations["owner"]) != 1 || !metrics.Relations["owner"][0].Forward {
		t.Fatalf("forward embed: %v", keys(metrics.Relations))
	}
	if len(users.Relations["stats"]) != 1 || users.Relations["stats"][0].Forward {
		t.Fatalf("backward embed: %v", keys(users.Relations))
	}
	if !metrics.FilterColumns["name"] || !metrics.FilterColumns["value"] {
		t.Fatalf("logical keys not filterable: %v", keys(metrics.FilterColumns))
	}
	if _, ok := b.Match("/metrics/users"); !ok {
		t.Fatal("/metrics/{name} does not route")
	}
}

func TestEnumTable(t *testing.T) {
	b := build(t, nil)
	if b.ByPath["event_type"] != nil {
		t.Fatal("enum table exposed as a resource")
	}
	e := b.Enums["public.event_type"]
	if e == nil || strings.Join(e.Values, ",") != "conference,hackathon,meetup" {
		t.Fatalf("enum = %+v", e)
	}
	s := b.OpenAPI.Components.Schemas
	if s["event_type"] == nil || s["event_type"].Description != "Kind of event.\n\nconference: A large formal gathering\nmeetup: Casual get-together" {
		t.Fatalf("enum schema = %+v", s["event_type"])
	}
	if ref := s["events"].Properties["type_code"].Ref; ref != "#/components/schemas/event_type" {
		t.Fatalf("type_code ref = %q", ref)
	}
	if len(b.ByPath["events"].Relations) != 0 {
		t.Fatal("enum FK generated embeds")
	}
}

func TestDescriptionsStripped(t *testing.T) {
	b := build(t, func(c *introspect.Catalog) {
		table(c, "posts").Comment = "@omit delete\n\n@name articles\nAll the posts.\nSecond line."
		column(c, "posts", "title").Comment = "@filterable\nThe title."
		function(c, "add_numbers").Comment = "@deprecated\nAdds."
	})
	articles := b.ByPath["articles"]
	if articles.Description != "All the posts.\nSecond line." {
		t.Fatalf("description = %q", articles.Description)
	}
	if articles.Field("title").Description != "The title." {
		t.Fatalf("column description = %q", articles.Field("title").Description)
	}
	if b.RPCByPath["add_numbers"].Description != "Adds." {
		t.Fatalf("rpc description = %q", b.RPCByPath["add_numbers"].Description)
	}
	if strings.Contains(openapi(t, b), "@") {
		t.Fatal("a tag line reached openapi")
	}
}

func TestDisabledLeavesRawSurface(t *testing.T) {
	cat := testutil.FixtureCatalog()
	table(cat, "users").Comment = "@name people\n@omit delete"
	column(cat, "users", "email").Comment = "@omit"
	p := smartcomments.New(nil)
	reg := plugin.NewRegistry(p)
	disabled := map[string]bool{"smart-comments": true}
	ctx := context.Background()
	if err := reg.TransformCatalog(ctx, disabled, cat); err != nil {
		t.Fatal(err)
	}
	b := api.New(cat, reg.Inflector(disabled, inflect.Default), api.Options{Schemas: []string{"public"}, Logger: quiet()})
	if err := reg.TransformAPI(ctx, disabled, b); err != nil {
		t.Fatal(err)
	}
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	users := built.ByPath["users"]
	if users == nil || built.ByPath["people"] != nil || !users.Methods[api.MethodDelete] || users.Field("email") == nil {
		t.Fatal("disabled plugin still applied tags")
	}
	if users.Description != "" {
		t.Fatalf("description = %q (tags are stripped by the builder even when disabled)", users.Description)
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
