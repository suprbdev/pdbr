// Package e2e runs the whole pipeline (introspect -> api -> HTTP) against a
// real database.
//
// It needs PDBR_TEST_DATABASE_URL pointing at a database initialized with
// db/init/01-schema.sql (make test-e2e brings one up); the tests are
// skipped otherwise, keeping `go test ./...` hermetic.
package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/auth"

	"github.com/suprbdev/pdbr"
	"github.com/suprbdev/pdbr/internal/config"
)

func testServer(t *testing.T, mutate func(*config.Config)) *httptest.Server {
	t.Helper()
	url := os.Getenv("PDBR_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	cfg := config.DefaultConfig()
	cfg.Database.URL = url
	cfg.Server.BasePath = "/api"
	cfg.UseConnectionIdentity()
	cfg.Errors.Detail = "dev"
	if mutate != nil {
		mutate(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	app := pdbr.New(cfg, pdbr.WithVersion("e2e"))
	t.Cleanup(app.Close)
	h, err := app.Handler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, method, url string, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res, body
}

func get(t *testing.T, url string) (*http.Response, []byte) {
	t.Helper()
	return do(t, http.MethodGet, url, nil)
}

// getJSON GETs url, requires status 200 and decodes the body into v.
func getJSON(t *testing.T, url string, v any) *http.Response {
	t.Helper()
	// json.Unmarshal merges into existing maps/slices; start from zero so a
	// reused destination cannot carry keys over from an earlier response.
	reflect.ValueOf(v).Elem().Set(reflect.Zero(reflect.TypeOf(v).Elem()))
	res, body := get(t, url)
	if res.StatusCode != 200 && res.StatusCode != 206 {
		t.Fatalf("GET %s = %d %s", url, res.StatusCode, body)
	}
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("GET %s: bad JSON %s: %v", url, body, err)
	}
	return res
}

type row = map[string]any

// sameJSON reports whether body and want encode the same JSON value,
// ignoring whitespace and object key order.
func sameJSON(body []byte, want string) bool {
	var a, b any
	if json.Unmarshal(body, &a) != nil || json.Unmarshal([]byte(want), &b) != nil {
		return false
	}
	return reflect.DeepEqual(a, b)
}

func ids(rows []row, key string) []int {
	out := make([]int, 0, len(rows))
	for _, r := range rows {
		out = append(out, int(r[key].(float64)))
	}
	return out
}

func eq(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type problem struct {
	Type     string         `json:"type"`
	Title    string         `json:"title"`
	Status   int            `json:"status"`
	Detail   string         `json:"detail"`
	Instance string         `json:"instance"`
	Code     string         `json:"code"`
	PG       map[string]any `json:"pg"`
}

func wantProblem(t *testing.T, res *http.Response, body []byte, status int, code string) problem {
	t.Helper()
	if res.StatusCode != status || res.Header.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("status = %d ct = %q body = %s, want %d %s", res.StatusCode, res.Header.Get("Content-Type"), body, status, code)
	}
	var p problem
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("not a problem body: %s", body)
	}
	if p.Code != code || p.Status != status || p.Type != "about:blank" || p.Title != http.StatusText(status) {
		t.Fatalf("problem = %+v, want %d %s", p, status, code)
	}
	return p
}

func TestHealthz(t *testing.T) {
	srv := testServer(t, nil)
	for _, p := range []string{"/healthz", "/readyz"} {
		res, body := get(t, srv.URL+p)
		if res.StatusCode != 200 || string(body) != `{"status":"ok"}` {
			t.Errorf("%s = %d %s", p, res.StatusCode, body)
		}
	}
}

func TestOpenAPIListsFixtureResources(t *testing.T) {
	srv := testServer(t, nil)
	res, body := get(t, srv.URL+"/api/openapi.json")
	if res.StatusCode != 200 {
		t.Fatalf("openapi = %d %s", res.StatusCode, body)
	}
	var doc struct {
		OpenAPI string                    `json:"openapi"`
		Info    map[string]string         `json:"info"`
		Servers []map[string]string       `json:"servers"`
		Paths   map[string]any            `json:"paths"`
		Comp    map[string]map[string]any `json:"components"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.OpenAPI != "3.1.0" || doc.Info["title"] != "pdbr" || doc.Info["version"] != "e2e" || doc.Servers[0]["url"] != "/api" {
		t.Errorf("top level = %+v", doc)
	}
	for _, name := range []string{"users", "posts", "comments", "events", "places", "metrics", "event_type", "mood", "address", "jwt", "Problem"} {
		if _, ok := doc.Comp["schemas"][name]; !ok {
			t.Errorf("components.schemas.%s missing", name)
		}
	}
	for _, p := range []string{"/users", "/users/{id}", "/users/by/email/{email}", "/posts", "/comments", "/events", "/places", "/metrics", "/rpc/search_posts", "/rpc/add_numbers", "/rpc/authenticate"} {
		if _, ok := doc.Paths[p]; !ok {
			t.Errorf("path %s missing", p)
		}
	}
	if _, ok := doc.Paths["/public/users"]; ok {
		t.Error("bare resources should be documented under the bare alias only")
	}
}

func TestRouting(t *testing.T) {
	srv := testServer(t, nil)
	res, body := get(t, srv.URL+"/api/nope")
	wantProblem(t, res, body, 404, "not_found")
	res, body = do(t, "POST", srv.URL+"/api/metrics", nil)
	if p := wantProblem(t, res, body, 405, "method_not_allowed"); res.Header.Get("Allow") != "GET, HEAD" || p.Instance != "/api/metrics" {
		t.Errorf("POST /api/metrics Allow %q problem %+v", res.Header.Get("Allow"), p)
	}
	// Multi-schema: the bare alias and the schema-qualified path are the
	// same resource.
	var bare, qual []row
	getJSON(t, srv.URL+"/api/users?select=id&order=id", &bare)
	getJSON(t, srv.URL+"/api/public/users?select=id&order=id", &qual)
	if len(bare) != 3 || !eq(ids(bare, "id"), ids(qual, "id")) {
		t.Errorf("bare %v qualified %v", bare, qual)
	}
	var one row
	getJSON(t, srv.URL+"/api/public/users/1?select=email", &one)
	if one["email"] != "ada@example.com" {
		t.Errorf("qualified row = %v", one)
	}
}

// ---- reads ----

func TestListSelectAndEmbeds(t *testing.T) {
	srv := testServer(t, nil)
	var rows []row
	res := getJSON(t, srv.URL+"/api/users?select=id,email,posts(id,title)&mood=eq.happy&limit=2", &rows)
	if res.Header.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Errorf("content-type = %q", res.Header.Get("Content-Type"))
	}
	if len(rows) != 2 || rows[0]["email"] != "ada@example.com" || rows[1]["email"] != "alan@example.com" {
		t.Fatalf("rows = %v", rows)
	}
	if posts := rows[0]["posts"].([]any); len(posts) != 2 || posts[0].(map[string]any)["title"] != "Notes on the Analytical Engine" {
		t.Errorf("ada posts = %v", posts)
	}
	if len(rows[0]) != 3 {
		t.Errorf("sparse fields leaked: %v", rows[0])
	}

	// forward embed + alias
	getJSON(t, srv.URL+"/api/posts?select=id,title,author:users(email)&order=id", &rows)
	if len(rows) != 4 || rows[0]["author"].(map[string]any)["email"] != "ada@example.com" {
		t.Errorf("forward = %v", rows)
	}
	// alias on a column
	getJSON(t, srv.URL+"/api/users?select=id,n:email&limit=1", &rows)
	if rows[0]["n"] != "ada@example.com" || rows[0]["email"] != nil {
		t.Errorf("alias = %v", rows)
	}
	// * plus computed
	getJSON(t, srv.URL+"/api/users?select=*,post_count&order=id", &rows)
	if rows[0]["post_count"] != "2" || rows[0]["full_name"] != "Ada Lovelace" || rows[1]["post_count"] != "1" {
		t.Errorf("computed = %v", rows)
	}
	// nested backward with per-embed params (order/limit/offset/filter)
	getJSON(t, srv.URL+"/api/users?select=id,posts(id,title,comments(id))&posts.order=id.desc&posts.limit=1&order=id", &rows)
	if p := rows[0]["posts"].([]any); len(p) != 1 || int(p[0].(map[string]any)["id"].(float64)) != 2 || len(p[0].(map[string]any)["comments"].([]any)) != 0 {
		t.Errorf("per-embed order/limit = %v", rows[0])
	}
	getJSON(t, srv.URL+"/api/users?select=id,posts(id)&posts.offset=1&posts.order=id&order=id", &rows)
	if p := rows[0]["posts"].([]any); len(p) != 1 || int(p[0].(map[string]any)["id"].(float64)) != 2 {
		t.Errorf("per-embed offset = %v", rows[0])
	}
	getJSON(t, srv.URL+"/api/users?select=id,posts(id)&posts.title=like.*draft*&order=id", &rows)
	if len(rows) != 3 || len(rows[0]["posts"].([]any)) != 1 || len(rows[1]["posts"].([]any)) != 0 {
		t.Errorf("embed filter keeps parents: %v", rows)
	}
	// !inner restricts the parent
	getJSON(t, srv.URL+"/api/users?select=id,posts!inner(id)&posts.title=like.*draft*", &rows)
	if len(rows) != 1 || int(rows[0]["id"].(float64)) != 1 {
		t.Errorf("inner = %v", rows)
	}
	getJSON(t, srv.URL+"/api/posts?select=title,users!inner(email)&users.email=eq.grace@example.com", &rows)
	if len(rows) != 1 || rows[0]["title"] != "Compilers 101" {
		t.Errorf("forward inner = %v", rows)
	}
	// hints on the self-referencing FK
	getJSON(t, srv.URL+"/api/comments?select=id,parent:comments!parent_id(id),replies:comments!comments_parent_id_fkey(id)&limit=1", &rows)
	if len(rows) != 0 { // fixture has no comments; the shape must still compile and run
		t.Errorf("comments = %v", rows)
	}
	res, body := get(t, srv.URL+"/api/comments?select=id,comments(id)")
	wantProblem(t, res, body, 400, "ambiguous_relation")
	res, body = get(t, srv.URL+"/api/users?select=id,posts(id,comments(id,posts(id,comments(id,posts(id,comments(id))))))")
	wantProblem(t, res, body, 400, "embed_depth_exceeded")
	res, body = get(t, srv.URL+"/api/users?select=id,nope")
	wantProblem(t, res, body, 400, "unknown_column")
	res, body = get(t, srv.URL+"/api/users?select=id,nope(id)")
	wantProblem(t, res, body, 400, "unknown_relation")
}

func TestFilters(t *testing.T) {
	srv := testServer(t, nil)
	cases := []struct {
		q    string
		want []int
	}{
		{"users?id=eq.2", []int{2}},
		{"users?id=neq.2&order=id", []int{1, 3}},
		{"users?id=gt.1&id=lte.3&order=id", []int{2, 3}},
		{"users?email=like.*grace*", []int{2}},
		{"users?email=ilike.ADA*", []int{1}},
		{"users?email=sw.al", []int{3}},
		{"users?email=ew.example.com&order=id", []int{1, 2, 3}},
		{"users?email=not.eq.ada@example.com&order=id", []int{2, 3}},
		{"users?id=in.(1,3)&order=id", []int{1, 3}},
		{"users?id=not.in.(1,3)", []int{2}},
		{"users?mood=is.null", []int{}},
		{"users?tags=is.null", []int{3}},
		{"users?tags=not.is.null&order=id", []int{1, 2}},
		{"users?mood=eq.happy&order=id", []int{1, 3}},
		{"users?mood=in.(ok,sad)", []int{2}},
		{"users?mood=not.eq.happy", []int{2}},
		{"users?moods=cs.{sad}", []int{1}},
		{"users?tags=cs.{admin,founder}", []int{1}},
		{"users?tags=cd.{staff,extra}", []int{2}},
		{"users?tags=ov.{staff,founder}&order=id", []int{1, 2}},
		{"users?settings=cs.{\"theme\":\"dark\"}", []int{1}},
		{"users?settings=hk.theme&order=id", []int{1, 2}},
		{"users?settings=jpe.$.theme&order=id", []int{1, 2}},
		{"users?settings=jpm.$.theme==\"light\"", []int{2}},
		{"users?or=(mood.eq.ok,email.eq.alan@example.com)&order=id", []int{2, 3}},
		{"users?and=(mood.eq.happy,id.gt.1)", []int{3}},
		{"users?not.or=(mood.eq.ok,id.eq.1)", []int{3}},
		{"users?and=(id.gte.1,or(mood.eq.ok,not.email.eq.ada@example.com))&order=id", []int{2, 3}},
		{"posts?published=is.true&order=id", nil}, // published is not indexed: 400 (checked below)
		{"posts?author_id=eq.1&order=id", []int{1, 2}},
		{"posts?title=sw.Notes", []int{1}},
		{"events?type_code=eq.meetup", []int{3}},
		{"events?type_code=in.(conference,hackathon)&order=id", []int{1, 2}},
		// relation filters (advanced_filters gate open)
		{"users?posts.title=eq.Compilers%20101", []int{2}},
		{"users?posts.title=sw.Un&posts.author_id=eq.1", []int{1}},
		{"users?posts!none.title=eq.Compilers%20101&order=id", []int{1, 3}},
		{"users?posts!every.author_id=eq.1&order=id", []int{1}},
		{"users?posts=exists.true&order=id", []int{1, 2, 3}},
		{"places?users.email=eq.grace@example.com", []int{3}},
		{"posts?users.email=eq.ada@example.com&order=id", []int{1, 2}},
		// computed filter/order
		{"users?post_count=gte.2", []int{1}},
		{"users?order=post_count.desc,id", []int{1, 2, 3}},
		{"posts?order=users(email).desc,id", []int{3, 4, 1, 2}},
		{"users?order=posts(title).desc", []int{1, 3, 2}},
	}
	for _, tc := range cases {
		res, body := get(t, srv.URL+"/api/"+tc.q)
		if tc.want == nil {
			wantProblem(t, res, body, 400, "column_not_filterable")
			continue
		}
		if res.StatusCode != 200 {
			t.Errorf("%s = %d %s", tc.q, res.StatusCode, body)
			continue
		}
		var rows []row
		if err := json.Unmarshal(body, &rows); err != nil {
			t.Fatal(err)
		}
		if got := ids(rows, "id"); !eq(got, tc.want) {
			t.Errorf("%s = %v, want %v", tc.q, got, tc.want)
		}
	}
	// grammar and policy errors
	for q, code := range map[string]string{
		"users?id=foo.1":              "invalid_filter",
		"users?id=eq.abc":             "invalid_filter",
		"users?nope=eq.1":             "unknown_column",
		"users?full_name=eq.x":        "column_not_filterable",
		"users?nope.title=eq.x":       "unknown_relation",
		"users?posts!bad.title=eq.x":  "unknown_relation",
		"posts?users!none.email=eq.x": "invalid_filter",
		"users?and=(":                 "invalid_filter",
	} {
		res, body := get(t, srv.URL+"/api/"+q)
		wantProblem(t, res, body, 400, code)
	}
	// filters.allow_columns opens a non-indexed column
	srv2 := testServer(t, func(c *config.Config) { c.Filters.AllowColumns = map[string][]string{"public.posts": {"published"}} })
	var rows []row
	getJSON(t, srv2.URL+"/api/posts?published=is.true&order=id", &rows)
	if !eq(ids(rows, "id"), []int{1, 3, 4}) {
		t.Errorf("allow_columns: %v", rows)
	}
	getJSON(t, srv2.URL+"/api/posts?published=is.false", &rows)
	if !eq(ids(rows, "id"), []int{2}) {
		t.Errorf("allow_columns is.false: %v", rows)
	}
	srv3 := testServer(t, func(c *config.Config) { c.Filters.IndexedOnly = false })
	getJSON(t, srv3.URL+"/api/users?full_name=eq.Ada%20Lovelace", &rows)
	if !eq(ids(rows, "id"), []int{1}) {
		t.Errorf("indexed_only=false: %v", rows)
	}
}

func TestOrderAndNulls(t *testing.T) {
	srv := testServer(t, nil)
	cases := map[string][]int{
		"users?order=email.desc":           {2, 3, 1},
		"users?order=mood.asc,id.desc":     {2, 3, 1},
		"users?order=mood.desc,id":         {1, 3, 2},
		"users?order=tags.asc":             {1, 2, 3},
		"users?order=tags.asc.nullsfirst":  {3, 1, 2},
		"users?order=tags.desc":            {2, 1, 3},
		"users?order=tags.desc.nullsfirst": {3, 2, 1},
		"posts?order=title.desc,id":        {2, 4, 1, 3},
	}
	for q, want := range cases {
		var rows []row
		getJSON(t, srv.URL+"/api/"+q, &rows)
		if got := ids(rows, "id"); !eq(got, want) {
			t.Errorf("%s = %v, want %v", q, got, want)
		}
	}
	res, body := get(t, srv.URL+"/api/users?order=full_name")
	wantProblem(t, res, body, 400, "column_not_orderable")
	res, body = get(t, srv.URL+"/api/users?order=id.sideways")
	wantProblem(t, res, body, 400, "invalid_parameter")
	res, body = get(t, srv.URL+"/api/users?order=nope")
	wantProblem(t, res, body, 400, "unknown_column")
}

func TestPagination(t *testing.T) {
	srv := testServer(t, nil)
	var rows []row
	res := getJSON(t, srv.URL+"/api/posts?order=title.asc&limit=2&count=true", &rows)
	if !eq(ids(rows, "id"), []int{3, 1}) || res.Header.Get("X-Total-Count") != "4" {
		t.Fatalf("page 1 = %v total %q", ids(rows, "id"), res.Header.Get("X-Total-Count"))
	}
	next := res.Header.Get("X-Next-Cursor")
	if next == "" || res.Header.Get("X-Prev-Cursor") != "" {
		t.Fatalf("page 1 cursors: %v", res.Header)
	}
	if want := `</api/posts?order=title.asc&limit=2&count=true&after=` + next + `>; rel="next"`; res.Header.Get("Link") != want {
		t.Fatalf("Link = %q\nwant %q", res.Header.Get("Link"), want)
	}
	// follow rel="next"
	link := strings.TrimSuffix(strings.TrimPrefix(res.Header.Get("Link"), "<"), `>; rel="next"`)
	res = getJSON(t, srv.URL+link, &rows)
	if !eq(ids(rows, "id"), []int{4, 2}) || res.Header.Get("X-Total-Count") != "4" {
		t.Fatalf("page 2 = %v", ids(rows, "id"))
	}
	if res.Header.Get("X-Next-Cursor") != "" || res.Header.Get("X-Prev-Cursor") == "" {
		t.Fatalf("page 2 cursors: %v", res.Header)
	}
	prev := res.Header.Get("X-Prev-Cursor")
	if want := `</api/posts?order=title.asc&limit=2&count=true&before=` + prev + `>; rel="prev"`; res.Header.Get("Link") != want {
		t.Fatalf("page 2 Link = %q\nwant %q", res.Header.Get("Link"), want)
	}
	// walk back with rel="prev": the first page again, in the same order
	res = getJSON(t, srv.URL+"/api/posts?order=title.asc&limit=2&count=true&before="+prev, &rows)
	if !eq(ids(rows, "id"), []int{3, 1}) || res.Header.Get("X-Next-Cursor") != next || res.Header.Get("X-Prev-Cursor") != "" {
		t.Fatalf("back to page 1 = %v %v", ids(rows, "id"), res.Header)
	}
	// keyset with a filter and desc order, page size 1, full walk
	var seen []int
	url := "/api/posts?order=id.desc&limit=1&author_id=neq.2"
	for i := 0; i < 5; i++ {
		res = getJSON(t, srv.URL+url, &rows)
		seen = append(seen, ids(rows, "id")...)
		n := res.Header.Get("X-Next-Cursor")
		if n == "" {
			break
		}
		url = "/api/posts?order=id.desc&limit=1&author_id=neq.2&after=" + n
	}
	if !eq(seen, []int{4, 2, 1}) {
		t.Errorf("walk = %v", seen)
	}
	// offset paging
	res = getJSON(t, srv.URL+"/api/posts?order=id&limit=2&offset=1", &rows)
	if !eq(ids(rows, "id"), []int{2, 3}) || res.Header.Get("X-Prev-Cursor") == "" || res.Header.Get("X-Next-Cursor") == "" {
		t.Errorf("offset page = %v %v", ids(rows, "id"), res.Header)
	}
	// offset-only resource (view): offset links, no cursors
	res = getJSON(t, srv.URL+"/api/metrics?limit=1", &rows)
	if len(rows) != 1 || res.Header.Get("X-Next-Cursor") != "" || res.Header.Get("Link") != `</api/metrics?limit=1&offset=1>; rel="next"` {
		t.Errorf("view page 1 = %v %v", rows, res.Header)
	}
	res = getJSON(t, srv.URL+"/api/metrics?limit=1&offset=1", &rows)
	if len(rows) != 1 || res.Header.Get("Link") != `</api/metrics?limit=1&offset=0>; rel="prev"` {
		t.Errorf("view page 2 = %v %v", rows, res.Header)
	}
	// count with distinct: groups
	res = getJSON(t, srv.URL+"/api/users?distinct=mood&select=mood&order=mood&count=true", &rows)
	if len(rows) != 2 || rows[0]["mood"] != "ok" || rows[1]["mood"] != "happy" || res.Header.Get("X-Total-Count") != "2" || res.Header.Get("X-Next-Cursor") != "" {
		t.Errorf("distinct = %v %v", rows, res.Header)
	}
	// HEAD with count is the cheapest total
	res, body := do(t, "HEAD", srv.URL+"/api/posts?count=true&limit=1", nil)
	if res.StatusCode != 206 || len(body) != 0 || res.Header.Get("X-Total-Count") != "4" || res.Header.Get("X-Next-Cursor") == "" {
		t.Errorf("HEAD = %d %q %v", res.StatusCode, body, res.Header)
	}
	// errors
	for q, code := range map[string]string{
		"posts?limit=101":                                    "page_size_exceeded",
		"posts?limit=0":                                      "invalid_parameter",
		"posts?offset=-1":                                    "invalid_parameter",
		"metrics?after=WyJwdWJsaWMucG9zdHMiLDJd":             "keyset_unsupported",
		"posts?after=nope":                                   "invalid_cursor",
		"posts?after=WyJwdWJsaWMudXNlcnMiLDJd":               "invalid_cursor",
		"posts?before=WyJwdWJsaWMucG9zdHMiLDJd&offset=1":     "keyset_unsupported",
		"users?distinct=mood&after=WyJwdWJsaWMudXNlcnMiLDJd": "keyset_unsupported",
		"users?select=id,posts(id)&posts.limit=101":          "page_size_exceeded",
	} {
		res, body := get(t, srv.URL+"/api/"+q)
		wantProblem(t, res, body, 400, code)
	}
	srv2 := testServer(t, func(c *config.Config) { c.Limits.MaxCost = 50 })
	res, body = get(t, srv2.URL+"/api/users?select=id,posts(id,comments(id))")
	wantProblem(t, res, body, 400, "cost_exceeded")
}

func TestRowsAndEtags(t *testing.T) {
	srv := testServer(t, nil)
	var one row
	res := getJSON(t, srv.URL+"/api/users/1?select=id,email,address", &one)
	if one["email"] != "ada@example.com" || one["address"].(map[string]any)["city"] != "London" || len(one) != 3 {
		t.Fatalf("row = %v", one)
	}
	etag := res.Header.Get("ETag")
	if !strings.HasPrefix(etag, `W/"`) || len(etag) != 3+32+1 {
		t.Fatalf("etag = %q", etag)
	}
	// ETag is a property of the row, not of select=
	res2 := getJSON(t, srv.URL+"/api/users/1", &one)
	if res2.Header.Get("ETag") != etag {
		t.Errorf("etag depends on select: %q vs %q", res2.Header.Get("ETag"), etag)
	}
	res, body := do(t, "GET", srv.URL+"/api/users/1", map[string]string{"If-None-Match": etag})
	if res.StatusCode != 304 || len(body) != 0 || res.Header.Get("ETag") != etag {
		t.Errorf("304: %d %q %v", res.StatusCode, body, res.Header)
	}
	res, body = do(t, "GET", srv.URL+"/api/users/1", map[string]string{"If-None-Match": `W/"stale"`})
	if res.StatusCode != 200 || len(body) == 0 {
		t.Errorf("stale: %d", res.StatusCode)
	}
	// by unique
	getJSON(t, srv.URL+"/api/users/by/email/ada%40example.com?select=id,post_count", &one)
	if int(one["id"].(float64)) != 1 || one["post_count"] != "2" {
		t.Errorf("by unique = %v", one)
	}
	// row with embed
	getJSON(t, srv.URL+"/api/posts/1?select=id,users(email)", &one)
	if one["users"].(map[string]any)["email"] != "ada@example.com" {
		t.Errorf("row embed = %v", one)
	}
	// 404s
	res, body = get(t, srv.URL+"/api/users/999")
	if p := wantProblem(t, res, body, 404, "not_found"); p.Instance != "/api/users/999" {
		t.Errorf("instance = %q", p.Instance)
	}
	res, body = get(t, srv.URL+"/api/users/by/email/nobody")
	wantProblem(t, res, body, 404, "not_found")
	res, body = get(t, srv.URL+"/api/users/by/full_name/x")
	wantProblem(t, res, body, 404, "not_found")
	res, body = get(t, srv.URL+"/api/users/abc")
	wantProblem(t, res, body, 400, "invalid_parameter")
	res, body = get(t, srv.URL+"/api/users/1?limit=2")
	wantProblem(t, res, body, 400, "invalid_parameter")
	// HEAD parity on rows
	res, body = do(t, "HEAD", srv.URL+"/api/users/1", nil)
	if res.StatusCode != 200 || len(body) != 0 || res.Header.Get("ETag") != etag {
		t.Errorf("HEAD row = %d %v", res.StatusCode, res.Header)
	}
	res, _ = do(t, "HEAD", srv.URL+"/api/users/999", nil)
	if res.StatusCode != 404 {
		t.Errorf("HEAD 404 = %d", res.StatusCode)
	}
	// etags off: no header, If-None-Match ignored
	srv2 := testServer(t, func(c *config.Config) { c.Server.Etags = false })
	res, body = do(t, "GET", srv2.URL+"/api/users/1", map[string]string{"If-None-Match": etag})
	if res.StatusCode != 200 || res.Header.Get("ETag") != "" || len(body) == 0 {
		t.Errorf("etags off: %d %v", res.StatusCode, res.Header)
	}
}

func TestValueRendering(t *testing.T) {
	srv := testServer(t, nil)
	var one row
	getJSON(t, srv.URL+"/api/users/1", &one)
	if one["balance"] != "0" {
		t.Errorf("int8 must be a string: %v", one["balance"])
	}
	if tags, ok := one["tags"].([]any); !ok || len(tags) != 2 {
		t.Errorf("tags = %v", one["tags"])
	}
	if prev := one["prev_addresses"].([]any); len(prev) != 1 || prev[0].(map[string]any)["street"] != "1 Ockham Park" {
		t.Errorf("composite array = %v", one["prev_addresses"])
	}
	if one["moods"].([]any)[0] != "sad" || one["mood"] != "happy" {
		t.Errorf("enums = %v %v", one["moods"], one["mood"])
	}
	getJSON(t, srv.URL+"/api/users/2", &one)
	if one["address"] != nil {
		t.Errorf("null composite = %v", one["address"])
	}
	var rows []row
	getJSON(t, srv.URL+"/api/events?select=id,type_code&order=id", &rows)
	if rows[0]["type_code"] != "conference" {
		t.Errorf("table enum = %v", rows)
	}
	getJSON(t, srv.URL+"/api/places?select=name,location&id=eq.1", &rows)
	loc := rows[0]["location"].(map[string]any)
	if loc["type"] != "Point" {
		t.Errorf("geojson = %v", loc)
	}
}

func TestContentNegotiation(t *testing.T) {
	srv := testServer(t, nil)
	res, body := do(t, "GET", srv.URL+"/api/users", map[string]string{"Accept": "application/xml"})
	wantProblem(t, res, body, 406, "not_acceptable")
	res, body = do(t, "GET", srv.URL+"/api/users/1", map[string]string{"Accept": "text/csv"})
	wantProblem(t, res, body, 406, "not_acceptable")
	for _, a := range []string{"*/*", "application/*", "application/json", "application/json;q=0.9, text/html"} {
		res, _ = do(t, "GET", srv.URL+"/api/users?limit=1", map[string]string{"Accept": a})
		if res.StatusCode != 200 {
			t.Errorf("Accept %q = %d", a, res.StatusCode)
		}
	}
}

func TestProblemShape(t *testing.T) {
	srv := testServer(t, nil)
	res, body := get(t, srv.URL+"/api/users?select=emial")
	p := wantProblem(t, res, body, 400, "unknown_column")
	if p.Instance != "/api/users" || !strings.Contains(p.Detail, `"emial"`) || p.PG != nil {
		t.Errorf("problem = %+v", p)
	}
	res, body = do(t, "HEAD", srv.URL+"/api/users?select=emial", nil)
	if res.StatusCode != 400 || len(body) != 0 || res.Header.Get("Content-Type") != "application/problem+json" {
		t.Errorf("HEAD problem = %d %q %v", res.StatusCode, body, res.Header)
	}
}

func TestDatabaseErrorMapping(t *testing.T) {
	// A bad enum label passes parse (labels are validated by the database
	// for `in` lists on table-backed enums) — use a jsonpath syntax error
	// instead, which the database rejects with a 42601 -> 400 pg:42601.
	srv := testServer(t, nil)
	res, body := get(t, srv.URL+"/api/users?settings=jpe.$[")
	p := wantProblem(t, res, body, 400, "pg:42601")
	if p.PG["sqlstate"] != "42601" || p.Detail == "invalid operation" {
		t.Errorf("dev problem = %+v", p)
	}
	srvProd := testServer(t, func(c *config.Config) { c.Errors.Detail = "prod" })
	res, body = get(t, srvProd.URL+"/api/users?settings=jpe.$[")
	if p = wantProblem(t, res, body, 400, "pg:42601"); p.Detail != "invalid operation" || p.PG != nil {
		t.Errorf("prod problem = %+v", p)
	}
}

// TestRLSMatrix: anonymous sees the 3 published posts, app_user #1 also
// sees their own draft (4), app_user #2 sees 3 (headers auth mode).
func TestRLSMatrix(t *testing.T) {
	srv := testServer(t, func(c *config.Config) {
		headersAuth(c)
	})
	count := func(hdr map[string]string) int {
		t.Helper()
		res, body := do(t, "GET", srv.URL+"/api/posts?select=id&limit=100", hdr)
		if res.StatusCode != 200 {
			t.Fatalf("posts = %d %s", res.StatusCode, body)
		}
		var rows []row
		if err := json.Unmarshal(body, &rows); err != nil {
			t.Fatal(err)
		}
		return len(rows)
	}
	if n := count(nil); n != 3 {
		t.Errorf("anonymous = %d, want 3", n)
	}
	if n := count(map[string]string{"X-Pdbr-Claim-Role": "app_user", "X-Pdbr-Claim-User-Id": "1"}); n != 4 {
		t.Errorf("app_user #1 = %d, want 4", n)
	}
	if n := count(map[string]string{"X-Pdbr-Claim-Role": "app_user", "X-Pdbr-Claim-User-Id": "2"}); n != 3 {
		t.Errorf("app_user #2 = %d, want 3", n)
	}
	// The count subquery and single-row paths respect the same policies.
	res, _ := do(t, "GET", srv.URL+"/api/posts?count=true&limit=1", nil)
	if res.Header.Get("X-Total-Count") != "3" {
		t.Errorf("anonymous total = %q", res.Header.Get("X-Total-Count"))
	}
	res, body := do(t, "GET", srv.URL+"/api/posts/2", nil)
	wantProblem(t, res, body, 404, "not_found")
	res, _ = do(t, "GET", srv.URL+"/api/posts/2", map[string]string{"X-Pdbr-Claim-Role": "app_user", "X-Pdbr-Claim-User-Id": "1"})
	if res.StatusCode != 200 {
		t.Errorf("owner sees own draft: %d", res.StatusCode)
	}
	// anonymous has no grant on comments -> 403 pg:42501
	res, body = do(t, "GET", srv.URL+"/api/comments", nil)
	wantProblem(t, res, body, 403, "pg:42501")
	// allowed_roles gate
	srv2 := testServer(t, func(c *config.Config) {
		headersAuth(c)
		c.Roles.Allowed = []string{"app_user"}
	})
	res, body = do(t, "GET", srv2.URL+"/api/posts", map[string]string{"X-Pdbr-Claim-Role": "postgres"})
	wantProblem(t, res, body, 403, "role_not_allowed")
}

// TestRLSRejectsRoleNone: set_config('role', 'none') is RESET ROLE, which
// would run the request as the (RLS-bypassing) connection role, so a claim
// naming none, public or a pg_ role is refused even without allowed_roles.
func TestRLSRejectsRoleNone(t *testing.T) {
	srv := testServer(t, func(c *config.Config) {
		headersAuth(c)
	})
	for _, role := range []string{"none", "None", "public", "pg_read_all_data"} {
		for _, path := range []string{"/api/posts?select=id&limit=100", "/api/posts/2"} {
			res, body := do(t, "GET", srv.URL+path, map[string]string{"X-Pdbr-Claim-Role": role, "X-Pdbr-Claim-User-Id": "1"})
			wantProblem(t, res, body, 403, "role_not_allowed")
		}
		res, body := do(t, "POST", srv.URL+"/api/posts", map[string]string{"X-Pdbr-Claim-Role": role, "Content-Type": "application/json"})
		wantProblem(t, res, body, 403, "role_not_allowed")
	}
}

// TestClaimHeaderSpelling: every claim has one header spelling, so a claim
// header with an underscore, a repeated one, two spellings of one claim, or
// (with rls.auth.header_claims) a claim outside the allowlist is a 401.
func TestClaimHeaderSpelling(t *testing.T) {
	srv := testServer(t, func(c *config.Config) {
		headersAuth(c)
		c.Auth.Headers.Claims = []string{"user_id"}
	})
	send := func(hdr http.Header) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequest("GET", srv.URL+"/api/posts?select=id&limit=100", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header = hdr
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res, body
	}
	ok := http.Header{"X-Pdbr-Claim-Role": {"app_user"}, "X-Pdbr-Claim-User-Id": {"1"}}
	if res, body := send(ok); res.StatusCode != 200 {
		t.Fatalf("canonical claim headers = %d %s", res.StatusCode, body)
	}
	for name, hdr := range map[string]http.Header{
		"underscore":    {"X-Pdbr-Claim-Role": {"app_user"}, "X-Pdbr-Claim-User_id": {"1"}},
		"repeated":      {"X-Pdbr-Claim-Role": {"app_user"}, "X-Pdbr-Claim-User-Id": {"1", "2"}},
		"two spellings": {"X-Pdbr-Claim-Role": {"app_user"}, "X-Pdbr-Claim-User-Id": {"1"}, "x-pdbr-claim-user-id": {"2"}},
		"not allowed":   {"X-Pdbr-Claim-Role": {"app_user"}, "X-Pdbr-Claim-User-Id": {"1"}, "X-Pdbr-Claim-Tenant": {"9"}},
	} {
		res, body := send(hdr)
		if res.StatusCode != http.StatusUnauthorized || !strings.Contains(string(body), `"invalid_token"`) {
			t.Errorf("%s: %d %s, want 401 invalid_token", name, res.StatusCode, body)
		}
	}
}

func TestReadOnly(t *testing.T) {
	srv := testServer(t, func(c *config.Config) { c.Server.ReadOnly = true })
	res, body := do(t, "POST", srv.URL+"/api/users", nil)
	if p := wantProblem(t, res, body, 405, "read_only"); res.Header.Get("Allow") != "GET, HEAD" || p.Instance != "/api/users" {
		t.Errorf("read-only: Allow %q %+v", res.Header.Get("Allow"), p)
	}
	res, _ = get(t, srv.URL+"/api/users?limit=1")
	if res.StatusCode != 200 {
		t.Errorf("reads still work: %d", res.StatusCode)
	}
}

func TestBasePathRoot(t *testing.T) {
	srv := testServer(t, func(c *config.Config) { c.Server.BasePath = "" })
	var rows []row
	res := getJSON(t, srv.URL+"/posts?limit=1&order=id", &rows)
	if len(rows) != 1 || !strings.HasPrefix(res.Header.Get("Link"), "</posts?limit=1&order=id&after=") {
		t.Errorf("root base path: %v %v", rows, res.Header)
	}
}

// TestKeyOrderFollowsSelect: response objects keep the select= order of
// their fields (json objects are built in pair order; embeds follow the
// fields), at the root, in embeds and in composites; a column selected
// twice appears once, other items sharing a key are a 400.
func TestKeyOrderFollowsSelect(t *testing.T) {
	srv := testServer(t, nil)
	res, body := get(t, srv.URL+"/api/users?select=title:email,id,posts(title,id),address&order=id&limit=1")
	if res.StatusCode != 200 {
		t.Fatalf("status %d %s", res.StatusCode, body)
	}
	s := string(body)
	order := []string{`"title"`, `"id"`, `"address"`, `"street"`, `"city"`, `"mood"`, `"posts"`, `"title"`, `"id"`}
	at := 0
	for _, k := range order {
		i := strings.Index(s[at:], k)
		if i < 0 {
			t.Fatalf("key %s missing or out of order in %s", k, s)
		}
		at += i + len(k)
	}
	res, body = get(t, srv.URL+"/api/users?select=id,email,email:full_name&order=id&limit=1")
	wantProblem(t, res, body, 400, "invalid_select")
	res, body = get(t, srv.URL+"/api/posts?select=excerpt(max_chars=5),excerpt:title")
	wantProblem(t, res, body, 400, "invalid_select")
	res, body = get(t, srv.URL+"/api/users?select=*,id,email,id&order=id&limit=1")
	if res.StatusCode != 200 || strings.Count(string(body), `"email"`) != 1 {
		t.Errorf("repeated column = %d %s", res.StatusCode, body)
	}
}

// headersAuth authenticates requests with claim headers sent by the test
// client (loopback is the trusted proxy), switching to the claimed role and
// running requests without credentials as the fixture's anonymous role.
func headersAuth(c *config.Config) {
	c.Auth.Mode = auth.ModeHeaders
	c.Auth.Headers.TrustedProxies = []string{"127.0.0.1", "::1"}
	c.Roles.Switch = true
	c.Roles.Anonymous = "anonymous"
}

// jwtAuth authenticates requests with HS256 JWTs signed with secret,
// switching to the claimed role and running requests without credentials
// as the fixture's anonymous role.
func jwtAuth(c *config.Config, secret string) {
	c.Auth.Mode = auth.ModeJWT
	c.Auth.JWT.Secret = secret
	c.Roles.Switch = true
	c.Roles.Anonymous = "anonymous"
}
