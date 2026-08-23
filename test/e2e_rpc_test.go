package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbr/internal/config"
)

// rpcCall issues a request with an optional JSON body and returns the
// response, its body, and the decoded JSON (nil when the body is empty or
// not JSON).
func rpcCall(t *testing.T, method, url, body string, hdr map[string]string) (*http.Response, []byte, any) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if len(out) > 0 {
		dec := json.NewDecoder(strings.NewReader(string(out)))
		dec.UseNumber()
		if dec.Decode(&v) != nil {
			v = nil
		}
	}
	return res, out, v
}

func rpcOK(t *testing.T, method, url, body string) any {
	t.Helper()
	res, out, v := rpcCall(t, method, url, body, nil)
	if res.StatusCode != 200 {
		t.Fatalf("%s %s = %d %s", method, url, res.StatusCode, out)
	}
	return v
}

var rpcFixturesOnce sync.Once

// rpcFixtures adds what the shared fixture lacks for RPC coverage: a void
// function and a set-returning computed column without extra arguments
// (users_recent_posts takes n, which v1 does not support). Idempotent.
func rpcFixtures(t *testing.T) {
	t.Helper()
	url := os.Getenv("PDBR_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	rpcFixturesOnce.Do(func() {
		ctx := context.Background()
		conn, err := pgx.Connect(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(ctx)
		for _, ddl := range []string{
			`CREATE OR REPLACE FUNCTION public.rpc_noop() RETURNS void VOLATILE LANGUAGE sql AS $$ SELECT $$`,
			`CREATE OR REPLACE FUNCTION public.users_latest_posts(u users) RETURNS SETOF posts STABLE LANGUAGE sql AS $$
			   SELECT * FROM posts WHERE author_id = u.id ORDER BY id DESC LIMIT 2 $$`,
			`GRANT EXECUTE ON FUNCTION public.rpc_noop(), public.users_latest_posts(users) TO anonymous, app_user`,
		} {
			if _, err := conn.Exec(ctx, ddl); err != nil {
				t.Fatalf("rpc fixture: %v", err)
			}
		}
	})
}

func TestRPCScalarAndEnums(t *testing.T) {
	rpcFixtures(t)
	srv := testServer(t, nil)
	cases := map[string]string{
		"/api/rpc/best_mood":              `"happy"`,
		"/api/rpc/all_moods":              `["sad", "ok"]`,
		"/api/rpc/mood_moods":             `["ok", "happy"]`,
		"/api/rpc/mood_moods?limit=1":     `["ok"]`,
		"/api/rpc/mood_moods?offset=1":    `["happy"]`,
		"/api/rpc/describe_mood?m=happy":  `"mood is happy"`,
		"/api/rpc/describe_mood":          `null`,
		"/api/rpc/search_posts?term=zzzz": `[]`,
	}
	for path, want := range cases {
		res, body, _ := rpcCall(t, "GET", srv.URL+path, "", nil)
		if res.StatusCode != 200 || string(body) != want {
			t.Errorf("GET %s = %d %s, want %s", path, res.StatusCode, body, want)
		}
		if res.Header.Get("Content-Type") != "application/json; charset=utf-8" {
			t.Errorf("GET %s content-type %q", path, res.Header.Get("Content-Type"))
		}
	}
	// HEAD mirrors GET without a body; POST on a stable function behaves like GET.
	res, body, _ := rpcCall(t, "HEAD", srv.URL+"/api/rpc/best_mood", "", nil)
	if res.StatusCode != 200 || len(body) != 0 {
		t.Errorf("HEAD = %d %q", res.StatusCode, body)
	}
	if v := rpcOK(t, "POST", srv.URL+"/api/rpc/describe_mood", `{"m": "sad"}`); v != "mood is sad" {
		t.Errorf("POST describe_mood = %v", v)
	}
	// Errors: bad enum label, unknown argument, non-object body, media type.
	res, body, _ = rpcCall(t, "GET", srv.URL+"/api/rpc/describe_mood?m=meh", "", nil)
	wantProblem(t, res, body, 400, "invalid_filter")
	res, body, _ = rpcCall(t, "GET", srv.URL+"/api/rpc/describe_mood?mood=happy", "", nil)
	wantProblem(t, res, body, 400, "unknown_argument")
	res, body, _ = rpcCall(t, "POST", srv.URL+"/api/rpc/describe_mood", `{"nope": 1}`, nil)
	wantProblem(t, res, body, 400, "unknown_argument")
	res, body, _ = rpcCall(t, "POST", srv.URL+"/api/rpc/describe_mood", `[1]`, nil)
	wantProblem(t, res, body, 400, "invalid_body")
	res, body, _ = rpcCall(t, "POST", srv.URL+"/api/rpc/describe_mood", "", map[string]string{"Content-Type": "text/plain"})
	if res.StatusCode != 200 {
		t.Errorf("empty body ignores content type: %d %s", res.StatusCode, body)
	}
	res, body, _ = rpcCall(t, "GET", srv.URL+"/api/rpc/nope", "", nil)
	wantProblem(t, res, body, 404, "not_found")
}

func TestRPCVolatile(t *testing.T) {
	rpcFixtures(t)
	srv := testServer(t, nil)
	// GET on a volatile function is not routed.
	res, body, _ := rpcCall(t, "GET", srv.URL+"/api/rpc/add_numbers?a=1&b=2", "", nil)
	if wantProblem(t, res, body, 405, "method_not_allowed"); res.Header.Get("Allow") != "POST" {
		t.Errorf("Allow = %q", res.Header.Get("Allow"))
	}
	res, _, _ = rpcCall(t, "HEAD", srv.URL+"/api/rpc/add_numbers", "", nil)
	if res.StatusCode != 405 || res.Header.Get("Allow") != "POST" {
		t.Errorf("HEAD volatile = %d Allow %q", res.StatusCode, res.Header.Get("Allow"))
	}
	if v := rpcOK(t, "POST", srv.URL+"/api/rpc/add_numbers", `{"a": 2, "b": 40}`); v != json.Number("42") {
		t.Errorf("add_numbers = %v", v)
	}
	if v := rpcOK(t, "POST", srv.URL+"/api/rpc/add_numbers", `{"a": "2", "b": 40}`); v != json.Number("42") {
		t.Errorf("add_numbers with string number = %v", v)
	}
	// A missing argument is NULL: a + NULL is NULL.
	if v := rpcOK(t, "POST", srv.URL+"/api/rpc/add_numbers", `{"a": 2}`); v != nil {
		t.Errorf("add_numbers missing b = %v", v)
	}
	if v := rpcOK(t, "POST", srv.URL+"/api/rpc/add_numbers", ""); v != nil {
		t.Errorf("add_numbers without body = %v", v)
	}
	if v := rpcOK(t, "POST", srv.URL+"/api/rpc/word_lengths", `{"words": ["a", "bb", "ccc"]}`); v != json.Number("3") {
		t.Errorf("word_lengths = %v", v)
	}
	if v := rpcOK(t, "POST", srv.URL+"/api/rpc/bump_mood", `{"m": "sad"}`); v != "ok" {
		t.Errorf("bump_mood = %v", v)
	}
	// void -> 204 without a body.
	res, body, _ = rpcCall(t, "POST", srv.URL+"/api/rpc/rpc_noop", "", nil)
	if res.StatusCode != 204 || len(body) != 0 || res.Header.Get("Content-Type") != "" {
		t.Errorf("void = %d %q ct=%q", res.StatusCode, body, res.Header.Get("Content-Type"))
	}
	// Row-returning volatile function with select= and embeds; restore the
	// fixture afterwards (unpublish) so the RLS matrix keeps its counts.
	v := rpcOK(t, "POST", srv.URL+"/api/rpc/publish_post?select=id,published,users(email)", `{"post_id": 2}`)
	got, _ := v.(map[string]any)
	if got["id"] != json.Number("2") || got["published"] != true || got["users"].(map[string]any)["email"] != "ada@example.com" {
		t.Errorf("publish_post = %v", v)
	}
	v = rpcOK(t, "POST", srv.URL+"/api/rpc/unpublish_post", `{"post_id": 2}`)
	if got, _ := v.(map[string]any); got["published"] != false || len(got) < 5 {
		t.Errorf("unpublish_post = %v", v)
	}
	// No matching row: the function returns NULL -> body null, 200.
	if v := rpcOK(t, "POST", srv.URL+"/api/rpc/publish_post", `{"post_id": 999999}`); v != nil {
		t.Errorf("publish_post miss = %v", v)
	}
	// Read-only mode refuses volatile functions (405 read_only, no Allow) but
	// keeps stable ones.
	ro := testServer(t, func(c *config.Config) { c.Server.ReadOnly = true })
	res, body, _ = rpcCall(t, "POST", ro.URL+"/api/rpc/add_numbers", `{"a": 1, "b": 1}`, nil)
	if wantProblem(t, res, body, 405, "read_only"); res.Header.Get("Allow") != "" {
		t.Errorf("read-only volatile Allow = %q", res.Header.Get("Allow"))
	}
	if v := rpcOK(t, "POST", ro.URL+"/api/rpc/describe_mood", `{"m": "ok"}`); v != "mood is ok" {
		t.Errorf("read-only stable POST = %v", v)
	}
}

func TestRPCSerializationRetry(t *testing.T) {
	rpcFixtures(t)
	// retry_probe raises 40001 on odd sequence values; the sequence survives
	// the rollback, so one retry always succeeds and the result is even.
	srv := testServer(t, func(c *config.Config) { c.TX.MaxRetries = 3 })
	for range 2 {
		v := rpcOK(t, "POST", srv.URL+"/api/rpc/retry_probe", "")
		n, ok := v.(json.Number)
		if !ok {
			t.Fatalf("retry_probe = %v", v)
		}
		if i, _ := n.Int64(); i%2 != 0 {
			t.Errorf("retry_probe = %s, want an even value (retried)", n)
		}
	}
	// Without retries the failure surfaces as 409 pg:40001; the next attempt
	// (even value) succeeds, so the probe stays deterministic afterwards.
	none := testServer(t, func(c *config.Config) { c.TX.MaxRetries = 0 })
	res, body, _ := rpcCall(t, "POST", none.URL+"/api/rpc/retry_probe", "", nil)
	if res.StatusCode == 200 {
		res, body, _ = rpcCall(t, "POST", none.URL+"/api/rpc/retry_probe", "", nil)
	}
	wantProblem(t, res, body, 409, "pg:40001")
	rpcOK(t, "POST", none.URL+"/api/rpc/retry_probe", "")
}

func TestRPCSetOfTable(t *testing.T) {
	rpcFixtures(t)
	srv := testServer(t, func(c *config.Config) {
		c.Filters.AllowColumns = map[string][]string{"public.posts": {"published"}}
	})
	var rows []row
	res := getJSON(t, srv.URL+"/api/rpc/search_posts?term=o&select=id,title,users(email)&order=id.desc&count=true", &rows)
	if !eq(ids(rows, "id"), []int{4, 3, 1}) || res.Header.Get("X-Total-Count") != "3" {
		t.Fatalf("search_posts = %v total %q", rows, res.Header.Get("X-Total-Count"))
	}
	if rows[0]["title"] != "On Computable Numbers" || rows[0]["users"].(map[string]any)["email"] != "alan@example.com" {
		t.Errorf("row 0 = %v", rows[0])
	}
	if res.Header.Get("Link") != "" || res.Header.Get("X-Next-Cursor") != "" {
		t.Errorf("single page must not link: %q %q", res.Header.Get("Link"), res.Header.Get("X-Next-Cursor"))
	}
	// Filters (incl. allow_columns) and offset paging with Link headers.
	res = getJSON(t, srv.URL+"/api/rpc/search_posts?term=o&published=is.true&order=id&limit=1&count=true", &rows)
	if !eq(ids(rows, "id"), []int{1}) || res.Header.Get("X-Total-Count") != "3" {
		t.Fatalf("page 1 = %v total %q", rows, res.Header.Get("X-Total-Count"))
	}
	if got := res.Header.Get("Link"); got != `</api/rpc/search_posts?term=o&published=is.true&order=id&limit=1&count=true&offset=1>; rel="next"` {
		t.Errorf("Link = %q", got)
	}
	if res.Header.Get("X-Next-Cursor") != "" {
		t.Error("SETOF functions must not mint cursors")
	}
	res = getJSON(t, srv.URL+"/api/rpc/search_posts?term=o&published=is.true&order=id&limit=1&offset=1", &rows)
	if !eq(ids(rows, "id"), []int{3}) {
		t.Fatalf("page 2 = %v", rows)
	}
	links := res.Header.Get("Link")
	if !strings.Contains(links, `offset=2>; rel="next"`) || !strings.Contains(links, `offset=0>; rel="prev"`) {
		t.Errorf("Link = %q", links)
	}
	// Argument by POST, filters by query; author_id relation filter; embeds with !inner.
	var out []row
	r2, body, v := rpcCall(t, "POST", srv.URL+"/api/rpc/search_posts?author_id=eq.1&select=id,users!inner(email)&users.email=eq.ada@example.com", `{"term": "o"}`, nil)
	if r2.StatusCode != 200 {
		t.Fatalf("POST search_posts = %d %s", r2.StatusCode, body)
	}
	if err := json.Unmarshal(body, &out); err != nil || !eq(ids(out, "id"), []int{1}) {
		t.Errorf("POST search_posts = %v (%v)", v, err)
	}
	// Cursors are refused; the wrong page size too.
	cur := base64.RawURLEncoding.EncodeToString([]byte(`["public.posts",1]`))
	r2, body, _ = rpcCall(t, "GET", srv.URL+"/api/rpc/search_posts?term=o&after="+cur, "", nil)
	wantProblem(t, r2, body, 400, "keyset_unsupported")
	r2, body, _ = rpcCall(t, "GET", srv.URL+"/api/rpc/search_posts?term=o&limit=101", "", nil)
	wantProblem(t, r2, body, 400, "page_size_exceeded")
	// Unknown argument on GET when the name is neither an argument nor a column.
	r2, body, _ = rpcCall(t, "GET", srv.URL+"/api/rpc/search_posts?term=o&nope=eq.1", "", nil)
	wantProblem(t, r2, body, 400, "unknown_column")
	// HEAD carries the count header only.
	r2, body, _ = rpcCall(t, "HEAD", srv.URL+"/api/rpc/search_posts?term=o&count=true", "", nil)
	if r2.StatusCode != 200 || len(body) != 0 || r2.Header.Get("X-Total-Count") != "3" {
		t.Errorf("HEAD = %d %q total %q", r2.StatusCode, body, r2.Header.Get("X-Total-Count"))
	}
}

func TestComputedColumns(t *testing.T) {
	rpcFixtures(t)
	srv := testServer(t, nil)
	var u row
	getJSON(t, srv.URL+"/api/users/1?select=id,post_count,tag_words,latest_posts(id,title)", &u)
	if u["post_count"] != "2" {
		t.Errorf("post_count = %#v (int8 renders as string)", u["post_count"])
	}
	if tw, _ := json.Marshal(u["tag_words"]); string(tw) != `["admin","founder"]` {
		t.Errorf("tag_words = %s", tw)
	}
	lp, _ := u["latest_posts"].([]any)
	if len(lp) != 2 || lp[0].(map[string]any)["id"] != float64(2) || len(lp[0].(map[string]any)) != 2 {
		t.Errorf("latest_posts = %v", u["latest_posts"])
	}
	var rows []row
	getJSON(t, srv.URL+"/api/users?select=id,post_count&order=id&limit=3", &rows)
	if len(rows) != 3 || rows[0]["post_count"] != "2" || rows[2]["post_count"] != "1" {
		t.Errorf("list post_count = %v", rows)
	}
	// Computed columns never appear in * and are select-only; recent_posts
	// (extra argument) and excerpt are not exposed in v1.
	getJSON(t, srv.URL+"/api/users/1", &u)
	if _, ok := u["post_count"]; ok {
		t.Error("computed column leaked into *")
	}
	for _, path := range []string{"/api/users/1?select=recent_posts", "/api/posts/1?select=excerpt", "/api/users/1?select=post_count(len=1)"} {
		res, body, _ := rpcCall(t, "GET", srv.URL+path, "", nil)
		if res.StatusCode != 400 {
			t.Errorf("GET %s = %d %s, want 400", path, res.StatusCode, body)
		}
	}
	// A computed column selected on a mutation response is evaluated over the
	// RETURNING row (ROW(...)::table cast).
	res, body, v := rpcCall(t, "POST", srv.URL+"/api/users?select=email,post_count,tag_words", `{"email": "computed-`+t.Name()+`@example.com", "tags": ["x"]}`, nil)
	if res.StatusCode != 201 {
		t.Fatalf("POST users = %d %s", res.StatusCode, body)
	}
	if got, _ := v.(map[string]any); got["post_count"] != "0" || len(got["tag_words"].([]any)) != 1 {
		t.Errorf("computed on mutation = %v", v)
	}
	if loc := res.Header.Get("Location"); loc != "" {
		rpcCall(t, "DELETE", srv.URL+loc, "", nil)
	}
	// Computed filter/order (advanced-filters surface, gates open until M5).
	getJSON(t, srv.URL+"/api/users?select=id&post_count=gte.2", &rows)
	if !eq(ids(rows, "id"), []int{1}) {
		t.Errorf("post_count filter = %v", rows)
	}
}

func TestFunctionsDisabled(t *testing.T) {
	rpcFixtures(t)
	srv := testServer(t, func(c *config.Config) { c.Schema.Functions = false })
	res, body, _ := rpcCall(t, "GET", srv.URL+"/api/rpc/best_mood", "", nil)
	wantProblem(t, res, body, 404, "not_found")
	res, body, _ = rpcCall(t, "POST", srv.URL+"/api/rpc/add_numbers", `{"a":1,"b":2}`, nil)
	wantProblem(t, res, body, 404, "not_found")
	res, body, _ = rpcCall(t, "GET", srv.URL+"/api/users/1?select=id,post_count", "", nil)
	wantProblem(t, res, body, 400, "unknown_column")
	var doc struct {
		Paths map[string]any `json:"paths"`
	}
	getJSON(t, srv.URL+"/api/openapi.json", &doc)
	for p := range doc.Paths {
		if strings.Contains(p, "/rpc/") {
			t.Errorf("rpc path %s present with schema.functions=false", p)
		}
	}
	// With functions on, the OpenAPI document lists RPC paths per volatility.
	on := testServer(t, nil)
	var full struct {
		Paths map[string]map[string]any `json:"paths"`
	}
	getJSON(t, on.URL+"/api/openapi.json", &full)
	if item := full.Paths["/rpc/search_posts"]; item["get"] == nil || item["post"] == nil || item["head"] == nil {
		t.Errorf("/rpc/search_posts = %v", item)
	}
	if item := full.Paths["/rpc/publish_post"]; item["get"] != nil || item["post"] == nil {
		t.Errorf("/rpc/publish_post = %v", item)
	}
	if _, ok := full.Paths["/rpc/users_post_count"]; ok {
		t.Error("computed-column function exposed as rpc")
	}
}

func TestJWTMintRoundTrip(t *testing.T) {
	rpcFixtures(t)
	const secret = "e2e-mint-secret"
	srv := testServer(t, func(c *config.Config) {
		c.RLS.Enabled = true
		c.RLS.Auth.Mode = "jwt"
		c.RLS.Auth.JWTSecret = secret
		c.RLS.Auth.JWTType = "public.jwt"
		c.RLS.Auth.JWTIssuer = "pdbr-e2e"
		c.RLS.AllowedRoles = []string{"app_user"}
	})
	// authenticate is volatile: POST only, minted into a token string.
	res, body, _ := rpcCall(t, "GET", srv.URL+"/api/rpc/authenticate?user_email=ada@example.com", "", nil)
	wantProblem(t, res, body, 405, "method_not_allowed")
	v := rpcOK(t, "POST", srv.URL+"/api/rpc/authenticate", `{"user_email": "ada@example.com"}`)
	tok, ok := v.(string)
	if !ok || strings.Count(tok, ".") != 2 {
		t.Fatalf("authenticate = %s", body)
	}
	if v := rpcOK(t, "POST", srv.URL+"/api/rpc/authenticate", `{"user_email": "nobody@example.com"}`); v != nil {
		t.Errorf("unknown email = %v, want null", v)
	}
	// The minted claims (role app_user, user_id 1) drive RLS: Ada sees her
	// draft, anonymous does not.
	count := func(hdr map[string]string) []int {
		t.Helper()
		res, body, _ := rpcCall(t, "GET", srv.URL+"/api/posts?select=id&order=id", "", hdr)
		if res.StatusCode != 200 {
			t.Fatalf("posts = %d %s", res.StatusCode, body)
		}
		var rows []row
		if err := json.Unmarshal(body, &rows); err != nil {
			t.Fatal(err)
		}
		return ids(rows, "id")
	}
	if got := count(nil); !eq(got, []int{1, 3, 4}) {
		t.Errorf("anonymous = %v", got)
	}
	if got := count(map[string]string{"Authorization": "Bearer " + tok}); !eq(got, []int{1, 2, 3, 4}) {
		t.Errorf("ada = %v", got)
	}
	// A token signed with another secret is refused.
	other := testServer(t, func(c *config.Config) {
		c.RLS.Enabled = true
		c.RLS.Auth.Mode = "jwt"
		c.RLS.Auth.JWTSecret = "different"
		c.RLS.Auth.JWTType = "public.jwt"
	})
	res, body, _ = rpcCall(t, "GET", other.URL+"/api/posts", "", map[string]string{"Authorization": "Bearer " + tok})
	wantProblem(t, res, body, 401, "invalid_token")
	// Without jwt_type the composite is returned as an object.
	plain := testServer(t, nil)
	v = rpcOK(t, "POST", plain.URL+"/api/rpc/authenticate", `{"user_email": "ada@example.com"}`)
	if got, _ := v.(map[string]any); got["role"] != "app_user" || got["user_id"] != json.Number("1") {
		t.Errorf("composite = %v", v)
	} else if _, isStr := got["exp"].(string); !isStr {
		t.Errorf("exp (int8) should render as a string in composite output: %#v", got["exp"])
	}
}
