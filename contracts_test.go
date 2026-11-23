package pdbr

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/confx"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/config"
	"github.com/suprbdev/pdbr/internal/server"
)

// contractCatalog is the shared fixture tagged for three contracts:
// internal (everything), public (no email, no comments, no post counts)
// and partner (default exclude: posts and search_posts only).
func contractCatalog(t *testing.T) *introspect.Catalog {
	t.Helper()
	cat := testutil.FixtureCatalog()
	users, posts, comments := cat.Table("public", "users"), cat.Table("public", "posts"), cat.Table("public", "comments")
	if users == nil || posts == nil || comments == nil {
		t.Fatal("fixture lacks users, posts or comments")
	}
	users.Column("email").Comment = "@behavior -contract:public"
	comments.Comment = "@contract internal"
	posts.Comment = "@behavior +contract:partner"
	for _, f := range cat.Functions {
		switch f.Name {
		case "users_post_count":
			f.Comment = "@behavior -contract:public"
		case "search_posts":
			f.Comment = "@behavior +contract:partner"
		}
	}
	return cat
}

func contractConfig() config.Config {
	cfg := config.DefaultConfig()
	cfg.UseConnectionIdentity()
	cfg.Server.BasePath = "/api"
	cfg.Contracts = []confx.Contract{{Name: "internal"}, {Name: "public"}, {Name: "partner", Default: "exclude", Path: "/partners"}}
	return cfg
}

type openAPIDoc struct {
	Servers []struct {
		URL string `json:"url"`
	} `json:"servers"`
	Paths      map[string]map[string]json.RawMessage `json:"paths"`
	Components struct {
		Schemas map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"schemas"`
	} `json:"components"`
}

func decodeOpenAPI(t *testing.T, built *api.Built) (openAPIDoc, string) {
	t.Helper()
	raw, err := built.OpenAPI.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var doc openAPIDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc, string(raw)
}

func TestContractOpenAPI(t *testing.T) {
	ctx := context.Background()
	app := New(contractConfig(), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	cat := contractCatalog(t)
	docs := map[string]openAPIDoc{}
	raws := map[string]string{}
	for _, name := range []string{"public", "partner", "internal", "public"} {
		built, err := app.BuildContractAPI(ctx, cat, name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		doc, raw := decodeOpenAPI(t, built)
		if prev, ok := raws[name]; ok && prev != raw {
			t.Fatalf("%s: rebuilding from the same catalog changed the document", name)
		}
		docs[name], raws[name] = doc, raw
	}

	for name, want := range map[string]string{"internal": "/api/internal", "public": "/api/public", "partner": "/partners"} {
		if s := docs[name].Servers; len(s) != 1 || s[0].URL != want {
			t.Errorf("%s servers = %+v, want %s", name, s, want)
		}
	}

	internal := docs["internal"]
	for _, prop := range []string{"email", "post_count"} {
		if _, ok := internal.Components.Schemas["users"].Properties[prop]; !ok {
			t.Errorf("internal users schema lacks %s", prop)
		}
	}
	if internal.Paths["/comments"] == nil || internal.Paths["/users/by/email/{email}"] == nil {
		t.Error("internal document lacks /comments or the by-email route")
	}

	public := docs["public"]
	for _, l := range strings.Split(raws["public"], ",") {
		if l = strings.ToLower(l); strings.Contains(l, "email") && !strings.Contains(l, "user_email") {
			t.Errorf("public document mentions the hidden email column: %s", l)
		}
	}
	if public.Paths["/comments"] != nil || public.Components.Schemas["comments"].Properties != nil {
		t.Error("public document exposes the internal comments table")
	}
	if _, ok := public.Components.Schemas["users"].Properties["post_count"]; ok {
		t.Error("public document exposes users_post_count")
	}
	if public.Paths["/users"] == nil || public.Paths["/posts"] == nil || public.Paths["/rpc/search_posts"] == nil {
		t.Error("public document lost untagged surface")
	}

	partner := docs["partner"]
	if partner.Paths["/posts"] == nil || partner.Paths["/rpc/search_posts"] == nil {
		t.Error("partner document lacks the tagged-in posts and search_posts")
	}
	if partner.Paths["/users"] != nil || partner.Paths["/comments"] != nil || partner.Paths["/rpc/add_numbers"] != nil {
		t.Error("partner document exposes untagged tables or functions")
	}
}

func TestContractFullAPIUnchanged(t *testing.T) {
	ctx := context.Background()
	quiet := WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	plain := config.DefaultConfig()
	plain.UseConnectionIdentity()
	plain.Server.BasePath = "/api"
	want, err := New(plain, quiet).BuildAPI(ctx, contractCatalog(t))
	if err != nil {
		t.Fatal(err)
	}
	got, err := New(contractConfig(), quiet).BuildContractAPI(ctx, contractCatalog(t), "")
	if err != nil {
		t.Fatal(err)
	}
	_, w := decodeOpenAPI(t, want)
	_, g := decodeOpenAPI(t, got)
	if g != w {
		t.Error("the full API of an App with contracts differs from one without")
	}
	if _, err := New(contractConfig(), quiet).BuildContractAPI(ctx, contractCatalog(t), "nope"); err == nil {
		t.Error("unknown contract accepted")
	}
}

func TestContractGroupRoutes(t *testing.T) {
	ctx := context.Background()
	cfg := contractConfig()
	app := New(cfg, WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	cat := contractCatalog(t)
	group := server.NewGroup(server.ConfigOf(cfg), app.Log)
	for _, ct := range cfg.Contracts {
		_, rt, err := app.contractRuntime(ctx, cat, ct.Name)
		if err != nil {
			t.Fatal(err)
		}
		scfg := server.ConfigOf(cfg)
		scfg.BasePath = cfg.ContractBase(ct)
		group.Add(server.New(scfg, nil, rt, app.Log))
	}
	srv := httptest.NewServer(group.Handler())
	defer srv.Close()
	for path, want := range map[string]int{
		"/api/public/openapi.json":         http.StatusOK,
		"/partners/openapi.json":           http.StatusOK,
		"/api/internal/comments?select=id": http.StatusServiceUnavailable,
		"/api/public/comments?select=id":   http.StatusNotFound,
		"/api/public/users?select=email":   http.StatusBadRequest,
		"/api/public/users?select=id":      http.StatusServiceUnavailable,
		"/partners/users?select=id":        http.StatusNotFound,
		"/api/users?select=id":             http.StatusNotFound,
		"/api/openapi.json":                http.StatusNotFound,
		"/healthz":                         http.StatusOK,
	} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != want {
			t.Errorf("GET %s = %d, want %d", path, res.StatusCode, want)
		}
	}
}
