package e2e

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"

	"github.com/suprbdev/pdbr/internal/config"
)

// oasSchema compiles the OpenAPI 3.1 meta-schema (test-only dependency; the
// schema JSON is vendored under testdata so the suite stays offline).
func oasSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "openapi-3.1-schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatal(err)
	}
	const id = "https://spec.openapis.org/oas/3.1/schema/2022-10-07"
	c := jsonschema.NewCompiler()
	if err := c.AddResource(id, doc); err != nil {
		t.Fatal(err)
	}
	return c.MustCompile(id)
}

func fetchOpenAPI(t *testing.T, srv string) ([]byte, map[string]any) {
	t.Helper()
	res, body := get(t, srv+"/api/openapi.json")
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("openapi.json = %d %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("openapi.json: %v", err)
	}
	return body, doc
}

func validateOpenAPI(t *testing.T, schema *jsonschema.Schema, body []byte) {
	t.Helper()
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(inst); err != nil {
		t.Fatalf("OpenAPI document does not validate against the 3.1 meta-schema:\n%v", err)
	}
}

func pathOp(doc map[string]any, path, method string) map[string]any {
	item, _ := doc["paths"].(map[string]any)[path].(map[string]any)
	op, _ := item[method].(map[string]any)
	if op == nil {
		return nil
	}
	resolved := map[string]any{}
	for k, v := range op {
		resolved[k] = v
	}
	if ps, ok := op["parameters"].([]any); ok {
		out := make([]any, len(ps))
		for i, p := range ps {
			out[i] = p
			if ref, ok := p.(map[string]any)["$ref"].(string); ok {
				comps, _ := doc["components"].(map[string]any)["parameters"].(map[string]any)
				out[i] = comps[strings.TrimPrefix(ref, "#/components/parameters/")]
			}
		}
		resolved["parameters"] = out
	}
	return resolved
}

func opParams(op map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	if op == nil {
		return out
	}
	ps, _ := op["parameters"].([]any)
	for _, p := range ps {
		pm := p.(map[string]any)
		out[pm["in"].(string)+":"+pm["name"].(string)] = pm
	}
	return out
}

func componentSchema(doc map[string]any, name string) map[string]any {
	s, _ := doc["components"].(map[string]any)["schemas"].(map[string]any)[name].(map[string]any)
	return s
}

func TestOpenAPIValidatesAgainstMetaSchema(t *testing.T) {
	schema := oasSchema(t)
	for name, mutate := range map[string]func(*config.Config){
		"default": nil,
		"jwt": func(c *config.Config) {
			jwtAuth(c, "e2e-secret-e2e-secret-e2e-secret")
			c.Auth.MintType = "public.jwt"
		},
		"root-no-csv": func(c *config.Config) {
			c.Server.BasePath = ""
			c.Server.CSV = false
		},
		"headers-plugins-off": func(c *config.Config) {
			headersAuth(c)
			c.Plugins.Disabled = []string{"postgis", "advanced_filters", "nested_mutations", "smart_comments"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := testServer(t, mutate)
			base := srv.URL + "/api"
			if name == "root-no-csv" {
				base = srv.URL
			}
			res, body := get(t, base+"/openapi.json")
			if res.StatusCode != 200 {
				t.Fatalf("openapi.json = %d", res.StatusCode)
			}
			validateOpenAPI(t, schema, body)
			var doc map[string]any
			_ = json.Unmarshal(body, &doc)
			if name == "jwt" {
				if ss, _ := doc["components"].(map[string]any)["securitySchemes"].(map[string]any); ss["bearerAuth"] == nil || doc["security"] == nil {
					t.Errorf("jwt mode: security missing (%v %v)", ss, doc["security"])
				}
				if pathOp(doc, "/users", "get")["responses"].(map[string]any)["401"] == nil {
					t.Error("jwt mode: 401 not documented")
				}
				if s := pathOp(doc, "/rpc/authenticate", "post")["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any); s["format"] != "jwt" {
					t.Errorf("authenticate lacks jwt format: %v", s)
				}
			} else if doc["security"] != nil {
				t.Errorf("%s: unexpected security %v", name, doc["security"])
			}
		})
	}
}

func TestOpenAPISurface(t *testing.T) {
	srv := testServer(t, nil)
	_, doc := fetchOpenAPI(t, srv.URL)
	for _, name := range []string{"users", "users.insert", "users.update", "users.upsert", "users.connect", "posts.insert", "GeoJSON", "Problem", "mood", "address", "event_type", "jwt"} {
		if componentSchema(doc, name) == nil {
			t.Errorf("components.schemas.%s missing", name)
		}
	}
	if componentSchema(doc, "metrics.insert") != nil {
		t.Error("view has an insert schema")
	}
	// Body schemas: required sets, readOnly columns excluded, nested input.
	ins := componentSchema(doc, "posts.insert")
	if req, _ := ins["required"].([]any); len(req) != 1 || req[0] != "title" {
		t.Errorf("posts.insert required = %v (author_id optional with nested users key)", req)
	}
	props := ins["properties"].(map[string]any)
	if props["id"] != nil || props["users"] == nil || props["users"].(map[string]any)["properties"].(map[string]any)["connect"] == nil {
		t.Errorf("posts.insert properties = %v", props)
	}
	if up := componentSchema(doc, "users.upsert"); up["required"].([]any)[0] != "email" {
		t.Errorf("users.upsert = %v", up)
	}
	// Spatial input admits GeoJSON or WKT.
	loc := componentSchema(doc, "places.insert")["properties"].(map[string]any)["location"].(map[string]any)
	if anyOf, _ := loc["anyOf"].([]any); len(anyOf) != 2 || anyOf[0].(map[string]any)["oneOf"] == nil {
		t.Errorf("places.insert.location = %v", loc)
	}
	// Paths and operations.
	for path, methods := range map[string][]string{
		"/users":                    {"get", "head", "post", "put"},
		"/users/{id}":               {"get", "head", "patch", "delete"},
		"/users/by/email/{email}":   {"get", "head", "patch", "delete"},
		"/metrics":                  {"get", "head"},
		"/rpc/search_posts":         {"get", "head", "post"},
		"/rpc/add_numbers":          {"post"},
		"/rpc/authenticate":         {"post"},
		"/comments/{id}":            {"get", "head", "patch", "delete"},
		"/places":                   {"get", "head", "post", "put"},
		"/events":                   {"get", "head", "post", "put"},
		"/rpc/mood_moods":           {"get", "head", "post"},
		"/rpc/users_post_count_off": nil,
	} {
		item, ok := doc["paths"].(map[string]any)[path].(map[string]any)
		if methods == nil {
			if ok {
				t.Errorf("unexpected path %s", path)
			}
			continue
		}
		if !ok {
			t.Errorf("path %s missing", path)
			continue
		}
		for _, m := range methods {
			if item[m] == nil {
				t.Errorf("%s lacks %s", path, m)
			}
		}
		if _, has := item["post"]; has != slicesContains(methods, "post") {
			t.Errorf("%s post presence mismatch", path)
		}
	}
	list := pathOp(doc, "/users", "get")
	params := opParams(list)
	for _, p := range []string{"select", "order", "limit", "offset", "after", "before", "count", "distinct", "and", "or", "not.and", "not.or", "id", "email", "mood"} {
		if params["query:"+p] == nil {
			t.Errorf("/users get lacks query param %s", p)
		}
	}
	if params["query:full_name"] != nil {
		t.Error("non-indexed column exposed as filter param")
	}
	if params["header:Accept"] == nil {
		t.Error("Accept header param missing (server.csv on)")
	}
	if ops, _ := params["query:email"]["x-pdbr-filter-ops"].([]any); len(ops) != 15 || ops[8] != "isdistinct" || ops[9] != "like" || ops[14] != "imatch" {
		t.Errorf("email ops = %v", ops)
	}
	desc, _ := list["description"].(string)
	if !strings.Contains(desc, "Relation filters") || !strings.Contains(desc, "post_count") {
		t.Errorf("advanced_filters prose missing: %q", desc)
	}
	ok200 := list["responses"].(map[string]any)["200"].(map[string]any)
	for _, h := range []string{"X-Total-Count", "X-Next-Cursor", "X-Prev-Cursor", "Link"} {
		if ok200["headers"].(map[string]any)[h] == nil {
			t.Errorf("list 200 lacks header %s", h)
		}
	}
	if ok200["content"].(map[string]any)["text/csv"] == nil {
		t.Error("list 200 lacks text/csv")
	}
	if list["responses"].(map[string]any)["405"] != nil || list["responses"].(map[string]any)["default"] == nil {
		t.Errorf("list responses = %v", list["responses"])
	}
	places := opParams(pathOp(doc, "/places", "get"))
	if places["query:near"] == nil {
		t.Error("near param missing on /places")
	}
	if ops, _ := places["query:location"]["x-pdbr-filter-ops"].([]any); len(ops) < 10 || !strings.Contains(fmtAny(ops), "st_intersects") {
		t.Errorf("location ops = %v", ops)
	}
	if !strings.Contains(places["query:select"]["description"].(string), "location_area") || !strings.Contains(places["query:select"]["description"].(string), "transform=") {
		t.Errorf("select description = %q", places["query:select"]["description"])
	}
	row := pathOp(doc, "/users/{id}", "get")
	if r := row["responses"].(map[string]any); r["304"] == nil || r["200"].(map[string]any)["headers"].(map[string]any)["ETag"] == nil || r["404"] == nil {
		t.Errorf("row responses = %v", r)
	}
	if opParams(pathOp(doc, "/users/{id}", "patch"))["header:If-Match"] == nil {
		t.Error("If-Match missing on PATCH")
	}
	rpc := pathOp(doc, "/rpc/search_posts", "get")
	if p := opParams(rpc); p["query:term"] == nil || p["query:select"] == nil || p["query:after"] != nil {
		t.Errorf("search_posts params = %v", p)
	}
	post := pathOp(doc, "/rpc/add_numbers", "post")
	if body := post["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any); body["properties"].(map[string]any)["a"] == nil {
		t.Errorf("add_numbers body = %v", body)
	}
	if pathOp(doc, "/rpc/add_numbers", "post")["responses"].(map[string]any)["405"] == nil {
		t.Error("volatile rpc lacks 405 (read-only)")
	}
	tags, _ := doc["tags"].([]any)
	names := map[string]bool{}
	for _, tg := range tags {
		names[tg.(map[string]any)["name"].(string)] = true
	}
	if names["public"] || !names["users"] || !names["rpc"] {
		t.Errorf("tags = %v", tags)
	}
	if got := pathOp(doc, "/users", "get")["tags"]; !reflect.DeepEqual(got, []any{"users"}) {
		t.Errorf("/users tags = %v", got)
	}
	if doc["components"].(map[string]any)["securitySchemes"] != nil {
		t.Error("securitySchemes present without jwt auth")
	}
}

func slicesContains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// TestOpenAPIPluginsDisabled: disabling each built-in removes exactly its
// surface from the document.
func TestOpenAPIPluginsDisabled(t *testing.T) {
	disabled := func(t *testing.T, name string) map[string]any {
		srv := testServer(t, func(c *config.Config) { c.Plugins.Disabled = []string{name} })
		_, doc := fetchOpenAPI(t, srv.URL)
		return doc
	}
	t.Run("postgis", func(t *testing.T) {
		doc := disabled(t, "postgis")
		if componentSchema(doc, "GeoJSON") != nil {
			t.Error("GeoJSON schema still present")
		}
		if componentSchema(doc, "places")["properties"].(map[string]any)["location_area"] != nil {
			t.Error("pseudo-column still present")
		}
		p := opParams(pathOp(doc, "/places", "get"))
		if p["query:near"] != nil {
			t.Error("near param still present")
		}
		if ops, _ := p["query:location"]["x-pdbr-filter-ops"].([]any); len(ops) != 1 || ops[0] != "is" {
			t.Errorf("location ops without postgis = %v", ops)
		}
		if loc := componentSchema(doc, "places.insert")["properties"].(map[string]any)["location"].(map[string]any); loc["anyOf"] != nil || loc["oneOf"] != nil {
			t.Errorf("spatial input still GeoJSON/WKT: %v", loc)
		}
	})
	t.Run("advanced_filters", func(t *testing.T) {
		doc := disabled(t, "advanced_filters")
		desc, _ := pathOp(doc, "/users", "get")["description"].(string)
		if strings.Contains(desc, "Relation filters") || strings.Contains(desc, "Computed-column filters") {
			t.Errorf("advanced prose still present: %q", desc)
		}
		if od := opParams(pathOp(doc, "/users", "get"))["query:order"]["description"].(string); strings.Contains(od, "rel(col)") || strings.Contains(od, "Computed:") {
			t.Errorf("order description still advertises advanced order: %q", od)
		}
	})
	t.Run("nested_mutations", func(t *testing.T) {
		doc := disabled(t, "nested_mutations")
		if componentSchema(doc, "users.connect") != nil {
			t.Error("connect schema still present")
		}
		ins := componentSchema(doc, "posts.insert")
		if ins["properties"].(map[string]any)["users"] != nil {
			t.Error("nested property still present")
		}
		if req, _ := ins["required"].([]any); len(req) != 2 {
			t.Errorf("author_id must be required again: %v", req)
		}
	})
	t.Run("smart_comments", func(t *testing.T) {
		smartFixture(t)
		on := testServer(t, nil)
		_, doc := fetchOpenAPI(t, on.URL)
		// @name notes, @omit delete, column @omit create / @deprecated / @omit,
		// @omit all with write methods -> POST/PUT only.
		if p := doc["paths"].(map[string]any); p["/notes"] == nil || p["/sc_notes"] != nil {
			t.Fatalf("renamed path missing: %v", keysOf(p))
		}
		if pathOp(doc, "/notes/{id}", "delete") != nil || pathOp(doc, "/notes/{id}", "patch") == nil {
			t.Error("@omit delete not reflected")
		}
		if ins := componentSchema(doc, "notes.insert")["properties"].(map[string]any); ins["created_at"] != nil || ins["secret"] != nil || ins["title"] == nil {
			t.Errorf("notes.insert = %v", keysOf(ins))
		}
		if up := componentSchema(doc, "notes.update")["properties"].(map[string]any); up["created_at"] == nil {
			t.Error("@omit create must not affect update")
		}
		if p := opParams(pathOp(doc, "/notes", "get")); p["query:title"] == nil || p["query:secret"] != nil || p["query:body"] != nil {
			t.Errorf("notes params = %v", keysOfParams(p))
		}
		if body := componentSchema(doc, "notes")["properties"].(map[string]any)["body"].(map[string]any); body["deprecated"] != true || body["x-deprecation-reason"] != "use title" {
			t.Errorf("notes.body = %v", body)
		}
		tags := doc["paths"].(map[string]any)["/sc_tags"].(map[string]any)
		if tags["get"] != nil || tags["post"] == nil || tags["put"] == nil {
			t.Errorf("@omit all path item = %v", keysOf(tags))
		}
		off := testServer(t, func(c *config.Config) { c.Plugins.Disabled = []string{"smart_comments"} })
		_, doc = fetchOpenAPI(t, off.URL)
		if p := doc["paths"].(map[string]any); p["/sc_notes"] == nil || p["/notes"] != nil || pathOp(doc, "/sc_notes/{id}", "delete") == nil || pathOp(doc, "/sc_tags", "get") == nil {
			t.Error("smart_comments off: comments still applied")
		}
	})
}

func fmtAny(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func keysOfParams(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestSchemaPrintMatchesServed runs the CLI (go run ./cmd/pdbr schema print)
// against the same database and compares byte-for-byte with the served
// document; --yaml must carry the same content.
func TestSchemaPrintMatchesServed(t *testing.T) {
	url := os.Getenv("PDBR_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	srv := testServer(t, nil)
	served, _ := fetchOpenAPI(t, srv.URL)
	run := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command("go", append([]string{"run", "-ldflags", "-X main.version=e2e", "./cmd/pdbr", "schema", "print",
			"--database.url", url, "--auth.mode=none", "--roles.switch=false"}, args...)...)
		cmd.Dir = ".."
		cmd.Env = append(os.Environ(), "PDBR_LOG_LEVEL=error")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("schema print %v: %v\n%s", args, err, stderr.String())
		}
		return out
	}
	printed := run()
	var printedDoc, servedDoc any
	if err := json.Unmarshal(printed, &printedDoc); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(served, &servedDoc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(printedDoc, servedDoc) {
		t.Errorf("schema print differs from GET /api/openapi.json (%d vs %d bytes)", len(printed), len(served))
	}
	var fromYAML, fromJSON any
	if err := yaml.Unmarshal(run("--yaml"), &fromYAML); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(served, &fromJSON); err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(normalizeYAML(fromYAML))
	b, _ := json.Marshal(fromJSON)
	if !bytes.Equal(a, b) {
		t.Errorf("schema print --yaml differs from the JSON document")
	}
}

// normalizeYAML turns yaml.v3's map[string]any/int/float scalars into the
// shapes encoding/json produces for the same document.
func normalizeYAML(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			x[k] = normalizeYAML(val)
		}
		return x
	case []any:
		for i, val := range x {
			x[i] = normalizeYAML(val)
		}
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	}
	return v
}
