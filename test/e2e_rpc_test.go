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
		"/api/rpc/search_posts?term=zzzz": `[]`,
	}
	for path, want := range cases {
		res, body, _ := rpcCall(t, "GET", srv.URL+path, "", nil)
		if res.StatusCode != 200 || !sameJSON(body, want) {
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
	wantProblem(t, res, body, 400, "invalid_argument")
	res, body, _ = rpcCall(t, "GET", srv.URL+"/api/rpc/describe_mood?mood=happy", "", nil)
	wantProblem(t, res, body, 400, "unknown_argument")
	res, body, _ = rpcCall(t, "POST", srv.URL+"/api/rpc/describe_mood", `{"nope": 1}`, nil)
	wantProblem(t, res, body, 400, "unknown_argument")
	res, body, _ = rpcCall(t, "POST", srv.URL+"/api/rpc/describe_mood", `[1]`, nil)
	wantProblem(t, res, body, 400, "invalid_body")
	res, body, _ = rpcCall(t, "POST", srv.URL+"/api/rpc/describe_mood", "", map[string]string{"Content-Type": "text/plain"})
	wantProblem(t, res, body, 400, "missing_argument")
	res, body, _ = rpcCall(t, "GET", srv.URL+"/api/rpc/describe_mood", "", nil)
	wantProblem(t, res, body, 400, "missing_argument")
	if v := rpcOK(t, "POST", srv.URL+"/api/rpc/describe_mood", `{"m": null}`); v != nil {
		t.Errorf("describe_mood(null) = %v", v)
	}
	res, body, _ = rpcCall(t, "GET", srv.URL+"/api/rpc/nope", "", nil)
	wantProblem(t, res, body, 404, "not_found")
}

// rpcArgFixtures creates functions with defaulted, variadic and defaulted
// computed-column arguments; dropped again after the test.
func rpcArgFixtures(t *testing.T) {
	t.Helper()
	url := os.Getenv("PDBR_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	ctx := context.Background()
	exec := func(ddl ...string) error {
		conn, err := pgx.Connect(ctx, url)
		if err != nil {
			return err
		}
		defer conn.Close(ctx)
		for _, d := range ddl {
			if _, err := conn.Exec(ctx, d); err != nil {
				return err
			}
		}
		return nil
	}
	drop := []string{
		`DROP FUNCTION IF EXISTS public.e2e_greet(text, text)`,
		`DROP FUNCTION IF EXISTS public.e2e_sum(int4, int4[])`,
		`DROP FUNCTION IF EXISTS public.e2e_tail(int4, int4[])`,
		`DROP FUNCTION IF EXISTS public.users_label(users, text)`,
	}
	if err := exec(drop...); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := exec(drop...); err != nil {
			t.Errorf("rpc arg fixture cleanup: %v", err)
		}
	})
	err := exec(
		`CREATE FUNCTION public.e2e_greet(name text DEFAULT 'world', greeting text DEFAULT 'hello') RETURNS text
		    IMMUTABLE LANGUAGE sql AS $$ SELECT greeting || ', ' || coalesce(name, '<null>') $$`,
		`CREATE FUNCTION public.e2e_sum(base int4, VARIADIC nums int4[]) RETURNS int4
		    IMMUTABLE LANGUAGE sql AS $$ SELECT base + coalesce((SELECT sum(n)::int4 FROM unnest(nums) n), 0) $$`,
		`CREATE FUNCTION public.e2e_tail(base int4 DEFAULT 100, VARIADIC nums int4[] DEFAULT '{}') RETURNS int4
		    IMMUTABLE LANGUAGE sql AS $$ SELECT base + coalesce(array_length(nums, 1), 0) $$`,
		`CREATE FUNCTION public.users_label(u users, sep text DEFAULT ': ') RETURNS text
		    STABLE LANGUAGE sql AS $$ SELECT u.id || sep || u.email $$`,
		`GRANT EXECUTE ON FUNCTION public.e2e_greet(text, text), public.e2e_sum(int4, int4[]),
		    public.e2e_tail(int4, int4[]), public.users_label(users, text) TO anonymous, app_user`,
	)
	if err != nil {
		t.Fatalf("rpc arg fixture: %v", err)
	}
}

func TestRPCDefaultsAndVariadic(t *testing.T) {
	rpcArgFixtures(t)
	srv := testServer(t, nil)
	for path, want := range map[string]string{
		"/api/rpc/e2e_greet":                       `"hello, world"`,
		"/api/rpc/e2e_greet?greeting=hi":           `"hi, world"`,
		"/api/rpc/e2e_greet?name=ada&greeting=hey": `"hey, ada"`,
		"/api/rpc/e2e_sum?base=1&nums=2&nums=3":    `6`,
		"/api/rpc/e2e_sum?nums=2&base=10":          `12`,
		"/api/rpc/e2e_tail":                        `100`,
		"/api/rpc/e2e_tail?base=1":                 `1`,
		"/api/rpc/e2e_tail?base=1&nums=5&nums=6":   `3`,
	} {
		res, body, _ := rpcCall(t, "GET", srv.URL+path, "", nil)
		if res.StatusCode != 200 || string(body) != want {
			t.Errorf("GET %s = %d %s, want %s", path, res.StatusCode, body, want)
		}
	}
	for body, want := range map[string]string{
		`{}`:                 `"hello, world"`,
		`{"name": null}`:     `"hello, <null>"`,
		`{"greeting": "yo"}`: `"yo, world"`,
	} {
		res, out, _ := rpcCall(t, "POST", srv.URL+"/api/rpc/e2e_greet", body, nil)
		if res.StatusCode != 200 || string(out) != want {
			t.Errorf("POST e2e_greet %s = %d %s, want %s", body, res.StatusCode, out, want)
		}
	}
	for body, want := range map[string]string{
		`{"base": 1, "nums": [2, 3, 4]}`: `10`,
		`{"base": 1, "nums": []}`:        `1`,
	} {
		res, out, _ := rpcCall(t, "POST", srv.URL+"/api/rpc/e2e_sum", body, nil)
		if res.StatusCode != 200 || string(out) != want {
			t.Errorf("POST e2e_sum %s = %d %s, want %s", body, res.StatusCode, out, want)
		}
	}
	res, body, _ := rpcCall(t, "GET", srv.URL+"/api/rpc/e2e_sum?base=1", "", nil)
	wantProblem(t, res, body, 400, "missing_argument")
	res, body, _ = rpcCall(t, "GET", srv.URL+"/api/rpc/e2e_tail?nums=1", "", nil)
	if p := wantProblem(t, res, body, 400, "missing_argument"); !strings.Contains(p.Detail, "base") {
		t.Errorf("variadic leading omission detail = %q", p.Detail)
	}
	res, body, _ = rpcCall(t, "GET", srv.URL+"/api/rpc/e2e_sum?base=1&nums=x", "", nil)
	wantProblem(t, res, body, 400, "invalid_argument")

	var rows []row
	getJSON(t, srv.URL+"/api/users?select=id,label&id=eq.1", &rows)
	if len(rows) != 1 || rows[0]["label"] != "1: ada@example.com" {
		t.Errorf("computed column with a defaulted argument = %v", rows)
	}

	raw, doc := fetchOpenAPI(t, srv.URL)
	validateOpenAPI(t, oasSchema(t), raw)
	get := pathOp(doc, "/rpc/e2e_sum", "get")
	for _, p := range get["parameters"].([]any) {
		pm := p.(map[string]any)
		switch pm["name"] {
		case "base":
			if pm["required"] != true {
				t.Errorf("e2e_sum base param = %v", pm)
			}
		case "nums":
			if pm["required"] != true || pm["schema"].(map[string]any)["type"] != "array" || pm["explode"] != true {
				t.Errorf("e2e_sum nums param = %v", pm)
			}
		}
	}
	greet := doc["paths"].(map[string]any)["/rpc/e2e_greet"].(map[string]any)["post"].(map[string]any)
	if rb := greet["requestBody"].(map[string]any); rb["required"] == true {
		t.Errorf("e2e_greet body required = %v", rb)
	}
	props := greet["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["properties"].(map[string]any)
	if d, _ := props["greeting"].(map[string]any)["description"].(string); !strings.Contains(d, "'hello'::text") {
		t.Errorf("e2e_greet greeting description = %q", d)
	}
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
	// An explicit null is NULL: a + NULL is NULL.
	if v := rpcOK(t, "POST", srv.URL+"/api/rpc/add_numbers", `{"a": 2, "b": null}`); v != nil {
		t.Errorf("add_numbers null b = %v", v)
	}
	res, body, _ = rpcCall(t, "POST", srv.URL+"/api/rpc/add_numbers", `{"a": 2}`, nil)
	if p := wantProblem(t, res, body, 400, "missing_argument"); !strings.Contains(p.Detail, "b") {
		t.Errorf("missing b detail = %q", p.Detail)
	}
	res, body, _ = rpcCall(t, "POST", srv.URL+"/api/rpc/add_numbers", "", nil)
	if p := wantProblem(t, res, body, 400, "missing_argument"); !strings.Contains(p.Detail, "a, b") {
		t.Errorf("missing a, b detail = %q", p.Detail)
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
	// Computed columns never appear in * and are select-only.
	getJSON(t, srv.URL+"/api/users/1", &u)
	if _, ok := u["post_count"]; ok {
		t.Error("computed column leaked into *")
	}
	for path, code := range map[string]string{
		"/api/users/1?select=recent_posts":         "missing_argument",
		"/api/posts/1?select=excerpt":              "missing_argument",
		"/api/posts/1?select=excerpt(len=1)":       "unknown_argument",
		"/api/posts/1?select=excerpt(max_chars=x)": "invalid_argument",
		"/api/users/1?select=post_count(len=1)":    "unknown_argument",
		"/api/posts?select=excerpt&excerpt=eq.a":   "column_not_filterable",
	} {
		res, body, _ := rpcCall(t, "GET", srv.URL+path, "", nil)
		wantProblem(t, res, body, 400, code)
	}
	var pr row
	getJSON(t, srv.URL+"/api/posts/1?select=id,excerpt(max_chars=5),short:excerpt(max_chars=\"3\")", &pr)
	if e, _ := pr["excerpt"].(string); len(e) != 5 {
		t.Errorf("excerpt(max_chars=5) = %v", pr)
	}
	if e, _ := pr["short"].(string); len(e) != 3 {
		t.Errorf("short:excerpt(max_chars=3) = %v", pr)
	}
	getJSON(t, srv.URL+"/api/users/1?select=id,recent_posts(n=1,id,title)", &u)
	if rp, _ := u["recent_posts"].([]any); len(rp) != 1 || len(rp[0].(map[string]any)) != 2 {
		t.Errorf("recent_posts(n=1,id,title) = %v", u["recent_posts"])
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
	// Computed filter/order (advanced_filters surface, gates open until M5).
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
		jwtAuth(c, secret)
		c.Auth.MintType = "public.jwt"
		c.Auth.JWT.Issuer = "pdbr-e2e"
		c.Roles.Allowed = []string{"app_user"}
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
		jwtAuth(c, "different")
		c.Auth.MintType = "public.jwt"
	})
	res, body, _ = rpcCall(t, "GET", other.URL+"/api/posts", "", map[string]string{"Authorization": "Bearer " + tok})
	wantProblem(t, res, body, 401, "invalid_token")
	// Without mint_type the composite is returned as an object.
	plain := testServer(t, nil)
	v = rpcOK(t, "POST", plain.URL+"/api/rpc/authenticate", `{"user_email": "ada@example.com"}`)
	if got, _ := v.(map[string]any); got["role"] != "app_user" || got["user_id"] != json.Number("1") {
		t.Errorf("composite = %v", v)
	} else if _, isStr := got["exp"].(string); !isStr {
		t.Errorf("exp (int8) should render as a string in composite output: %#v", got["exp"])
	}
}

// recordFixtures creates functions returning record rows described by
// output parameters (issue 7); dropped again after the test.
func recordFixtures(t *testing.T) {
	t.Helper()
	url := os.Getenv("PDBR_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	ctx := context.Background()
	run := func(ddl ...string) error {
		conn, err := pgx.Connect(ctx, url)
		if err != nil {
			return err
		}
		defer conn.Close(ctx)
		for _, d := range ddl {
			if _, err := conn.Exec(ctx, d); err != nil {
				return err
			}
		}
		return nil
	}
	drop := []string{
		`DROP FUNCTION IF EXISTS public.mood_counts()`,
		`DROP FUNCTION IF EXISTS public.user_stats(int4)`,
	}
	if err := run(drop...); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := run(drop...); err != nil {
			t.Errorf("record fixture cleanup: %v", err)
		}
	})
	err := run(
		`CREATE FUNCTION public.mood_counts() RETURNS TABLE(mood mood, n bigint) STABLE LANGUAGE sql AS $$
		    SELECT u.mood, count(*) FROM users u WHERE u.mood IS NOT NULL GROUP BY u.mood $$`,
		`CREATE FUNCTION public.user_stats(uid int4, OUT posts bigint, OUT last_title text) STABLE LANGUAGE sql AS $$
		    SELECT count(*), max(title) FILTER (WHERE id = (SELECT max(id) FROM posts WHERE author_id = uid))
		    FROM posts WHERE author_id = uid $$`,
		`GRANT EXECUTE ON FUNCTION public.mood_counts(), public.user_stats(int4) TO anonymous, app_user`,
	)
	if err != nil {
		t.Fatalf("record fixture: %v", err)
	}
}

func TestRPCRecordReturning(t *testing.T) {
	recordFixtures(t)
	srv := testServer(t, nil)
	for path, want := range map[string]string{
		"/api/rpc/mood_counts?order=mood":                  `[{"n": "1", "mood": "ok"}, {"n": "2", "mood": "happy"}]`,
		"/api/rpc/mood_counts?select=mood&order=n.asc":     `[{"mood": "ok"}, {"mood": "happy"}]`,
		"/api/rpc/mood_counts?select=c:n&n=gt.1":           `[{"c": "2"}]`,
		"/api/rpc/mood_counts?order=mood&limit=1&offset=1": `[{"n": "2", "mood": "happy"}]`,
		"/api/rpc/user_stats?uid=1":                        `{"posts": "2", "last_title": "Unpublished draft"}`,
		"/api/rpc/user_stats?uid=999":                      `{"posts": "0", "last_title": null}`,
		"/api/rpc/user_stats?uid=1&select=posts":           `{"posts": "2"}`,
	} {
		res, body, _ := rpcCall(t, "GET", srv.URL+path, "", nil)
		if res.StatusCode != 200 || !sameJSON(body, want) {
			t.Errorf("GET %s = %d %s, want %s", path, res.StatusCode, body, want)
		}
	}
	res, _, _ := rpcCall(t, "GET", srv.URL+"/api/rpc/mood_counts?count=true&n=gte.1", "", nil)
	if res.Header.Get("X-Total-Count") != "2" {
		t.Errorf("X-Total-Count = %q", res.Header.Get("X-Total-Count"))
	}
	res, body, _ := rpcCall(t, "GET", srv.URL+"/api/rpc/mood_counts?order=mood", "", map[string]string{"Accept": "text/csv"})
	if res.StatusCode != 200 || string(body) != "mood,n\r\nok,1\r\nhappy,2\r\n" {
		t.Errorf("CSV = %d %q", res.StatusCode, body)
	}
	for path, code := range map[string]string{
		"/api/rpc/mood_counts?select=nope":      "unknown_column",
		"/api/rpc/mood_counts?select=users(id)": "unknown_relation",
		"/api/rpc/mood_counts?after=x":          "keyset_unsupported",
		"/api/rpc/user_stats":                   "missing_argument",
	} {
		res, body, _ := rpcCall(t, "GET", srv.URL+path, "", nil)
		wantProblem(t, res, body, 400, code)
	}
	raw, doc := fetchOpenAPI(t, srv.URL)
	validateOpenAPI(t, oasSchema(t), raw)
	if s, _ := schemas(doc)["rpc.mood_counts"].(map[string]any); s == nil || s["properties"].(map[string]any)["n"] == nil {
		t.Errorf("rpc.mood_counts schema = %v", s)
	}
	paths := doc["paths"].(map[string]any)
	ok := paths["/rpc/user_stats"].(map[string]any)["get"].(map[string]any)["responses"].(map[string]any)["200"].(map[string]any)
	if ref := ok["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["$ref"]; ref != "#/components/schemas/rpc.user_stats" {
		t.Errorf("user_stats 200 schema = %v", ok)
	}
}
