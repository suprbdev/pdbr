package smartcomments_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/parse"
)

var omitReadOpts = parse.Options{
	MaxEmbedDepth: 5, MaxPageSize: 100, Aggregates: true, RelationFilters: true,
	ComputedFilters: true, RelationOrder: true, ComputedOrder: true, Spatial: true,
}

func omitReadBuild(t *testing.T) *api.Built {
	t.Helper()
	return build(t, func(c *introspect.Catalog) {
		column(c, "users", "balance").Comment = "@omit read"
		column(c, "users", "settings").Comment = "@omit read"
		column(c, "users", "email").Comment = "@omit read"
	})
}

func TestOmitReadHidesColumnFromQuerySurfaces(t *testing.T) {
	b := omitReadBuild(t)
	users := b.ByPath["users"]
	for _, k := range []string{"balance", "settings", "email"} {
		if users.FilterColumns[k] || users.OrderColumns[k] {
			t.Errorf("%s still filterable or orderable", k)
		}
	}
	cases := []struct {
		name, url, code string
	}{
		{"filter", "/api/users?balance=gt.5", "unknown_column"},
		{"filter negated", "/api/users?balance=not.gt.5", "unknown_column"},
		{"filter text", "/api/users?email=like.a*", "unknown_column"},
		{"or tree", "/api/users?or=(id.eq.1,balance.gt.5)", "unknown_column"},
		{"nested logic", "/api/users?and=(id.gt.0,or(balance.lt.1,id.eq.2))", "unknown_column"},
		{"not logic", "/api/users?not.and=(balance.gt.1,id.gt.0)", "unknown_column"},
		{"json path", "/api/users?settings->>a=eq.x", "unknown_column"},
		{"json path jsonb", "/api/users?settings->a=eq.1", "unknown_column"},
		{"order", "/api/users?order=balance.desc", "unknown_column"},
		{"order second term", "/api/users?order=id.asc,balance.desc", "unknown_column"},
		{"distinct", "/api/users?distinct=balance", "unknown_column"},
		{"aggregate input", "/api/users?select=balance.sum()", "unknown_column"},
		{"aggregate grouping", "/api/users?select=balance,count()", "unknown_column"},
		{"aggregate on filter", "/api/users?select=count()&balance=gt.1", "unknown_column"},
		{"select", "/api/users?select=balance", "unknown_column"},
		{"embed filter", "/api/posts?select=title,users(id)&users.balance=gt.1", "unknown_column"},
		{"embed order", "/api/posts?select=title,users(id)&users.order=balance", "unknown_column"},
		{"embed filter in select", "/api/posts?select=title,users(id)&users.email=eq.a", "unknown_column"},
		{"relation filter", "/api/posts?users.balance=gt.1", "unknown_column"},
		{"relation order", "/api/posts?order=users(balance).asc", "unknown_column"},
		{"spread", "/api/posts?select=title,...users(balance)", "unknown_column"},
		{"on_conflict", "/api/users?on_conflict=email", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			method := http.MethodGet
			var body *strings.Reader
			if tc.name == "on_conflict" {
				method = http.MethodPut
				body = strings.NewReader(`{"email":"x"}`)
			} else {
				body = strings.NewReader("")
			}
			req := httptest.NewRequest(method, tc.url, body)
			req.Header.Set("Content-Type", "application/json")
			m, ok := b.Match(strings.TrimPrefix(req.URL.Path, "/api"))
			if !ok {
				t.Fatalf("no route for %s", tc.url)
			}
			_, err := parse.Parse(b, m, req, omitReadOpts)
			if err == nil {
				t.Fatalf("%s parsed; want 400", tc.url)
			}
			if tc.code != "" && !strings.Contains(err.Error(), "unknown column") {
				t.Fatalf("%s err = %v, want unknown column", tc.url, err)
			}
		})
	}
}

func TestOmitReadUniqueRoutes(t *testing.T) {
	b := omitReadBuild(t)
	for _, path := range []string{"/users/by/email/a%40b.c", "/users/by/email/a%40b.c/x"} {
		if _, ok := b.Match(path); ok {
			t.Errorf("%s still routes", path)
		}
	}
	if len(b.ByPath["users"].Uniques) != 0 {
		t.Error("unique over a hidden column survives")
	}
}

func TestOmitReadPrimaryKey(t *testing.T) {
	b := build(t, func(c *introspect.Catalog) {
		column(c, "users", "id").Comment = "@omit read"
	})
	users := b.ByPath["users"]
	if !users.HiddenKey || users.Keyset {
		t.Fatalf("HiddenKey = %v Keyset = %v", users.HiddenKey, users.Keyset)
	}
	if _, ok := b.Match("/users/1"); ok {
		t.Error("row path survives a hidden primary key")
	}
	cur := "WyJwdWJsaWMudXNlcnMiLDFd"
	for _, p := range []string{"after", "before"} {
		req := httptest.NewRequest(http.MethodGet, "/api/users?"+p+"="+cur, nil)
		m, _ := b.Match("/users")
		if _, err := parse.Parse(b, m, req, omitReadOpts); err == nil || !strings.Contains(err.Error(), "primary key") {
			t.Errorf("%s= err = %v, want keyset unsupported", p, err)
		}
	}
	if b.OpenAPI.Paths["/users/{id}"] != nil {
		t.Error("openapi documents the row path of a hidden key")
	}
	if _, ok := b.OpenAPI.Paths["/users"].Get.Responses["200"].Headers["X-Next-Cursor"]; ok {
		t.Error("openapi documents cursors of a hidden key")
	}
	for _, p := range b.OpenAPI.Paths["/users"].Get.Parameters {
		if p.Name == "after" || p.Name == "before" {
			t.Errorf("openapi documents %s= of a hidden key", p.Name)
		}
	}
}

func TestOmitReadOpenAPI(t *testing.T) {
	b := omitReadBuild(t)
	doc := b.OpenAPI
	list := doc.Paths["/users"].Get
	for _, p := range list.Parameters {
		name := p.Name
		if p.Ref != "" {
			name = p.Ref
		}
		for _, hidden := range []string{"balance", "settings", "email"} {
			if name == hidden || strings.HasSuffix(name, "/"+hidden) || strings.Contains(name, "_"+hidden) {
				t.Errorf("GET /users documents hidden column parameter %q", name)
			}
		}
	}
	js := openapi(t, b)
	if strings.Contains(js, "/users/by/") {
		t.Error("unique route over a hidden column documented")
	}
}
