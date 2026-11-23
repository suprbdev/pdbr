package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbr/internal/config"
)

// sweepNested removes what a nested-mutation test left behind (children
// first) so the read suite keeps seeing the pristine fixture.
func sweepNested(t *testing.T) {
	t.Helper()
	url := os.Getenv("PDBR_TEST_DATABASE_URL")
	if url == "" {
		return
	}
	t.Cleanup(func() {
		ctx := context.Background()
		conn, err := pgx.Connect(ctx, url)
		if err != nil {
			t.Logf("sweep: %v", err)
			return
		}
		defer conn.Close(ctx)
		for _, q := range []string{
			`DELETE FROM comments WHERE body LIKE 'M5N %' OR post_id IN (SELECT id FROM posts WHERE title LIKE 'M5N %' OR author_id IN (SELECT id FROM users WHERE email LIKE 'm5n-%'))`,
			`DELETE FROM places WHERE name LIKE 'M5N %' OR owner_id IN (SELECT id FROM users WHERE email LIKE 'm5n-%')`,
			`DELETE FROM posts WHERE title LIKE 'M5N %' OR author_id IN (SELECT id FROM users WHERE email LIKE 'm5n-%')`,
			`DELETE FROM users WHERE email LIKE 'm5n-%'`,
		} {
			if _, err := conn.Exec(ctx, q); err != nil {
				t.Logf("sweep: %v", err)
			}
		}
	})
}

var nestedSeq int

func nestedEmail(t *testing.T) string {
	nestedSeq++
	return fmt.Sprintf("m5n-%d-%d-%s@example.com", time.Now().UnixNano(), nestedSeq, strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")))
}

func TestNestedForwardCreate(t *testing.T) {
	sweepNested(t)
	srv := testServer(t, nil)
	email := nestedEmail(t)
	var post row
	res := sendJSON(t, "POST", srv.URL+"/api/posts?select=id,title,author_id,users(id,email,full_name)",
		`{"title":"M5N fwd","users":{"create":{"email":"`+email+`","full_name":"Nested"}}}`, 201, &post)
	if res.Header.Get("Location") != fmt.Sprintf("/api/posts/%d", rowID(t, post)) {
		t.Fatalf("Location = %q", res.Header.Get("Location"))
	}
	u, _ := post["users"].(map[string]any)
	if u == nil || u["email"] != email || u["full_name"] != "Nested" || post["author_id"] != u["id"] {
		t.Fatalf("response embed does not show the created parent: %v", post)
	}
	var user row
	getJSON(t, srv.URL+"/api/users/by/email/"+email+"?select=id,email,posts(id,title)", &user)
	posts, _ := user["posts"].([]any)
	if len(posts) != 1 || posts[0].(map[string]any)["title"] != "M5N fwd" {
		t.Fatalf("parent row not linked: %v", user)
	}
}

func TestNestedConnect(t *testing.T) {
	sweepNested(t)
	srv := testServer(t, nil)
	t.Run("by primary key with embed", func(t *testing.T) {
		var post row
		sendJSON(t, "POST", srv.URL+"/api/posts?select=id,author_id,users(id,email)", `{"title":"M5N conn","users":{"connect":{"id":1}}}`, 201, &post)
		if int(post["author_id"].(float64)) != 1 || post["users"].(map[string]any)["email"] != "ada@example.com" {
			t.Fatalf("connect: %v", post)
		}
	})
	t.Run("by unique column", func(t *testing.T) {
		var post row
		sendJSON(t, "POST", srv.URL+"/api/posts?select=id,author_id", `{"title":"M5N conn","users":{"connect":{"email":"grace@example.com"}}}`, 201, &post)
		if int(post["author_id"].(float64)) != 2 {
			t.Fatalf("connect by email: %v", post)
		}
	})
	t.Run("minimal (docs/api.md example)", func(t *testing.T) {
		res, body := send(t, "POST", srv.URL+"/api/posts", `{"title":"M5N min","published":false,"users":{"connect":{"id":1}}}`, map[string]string{"Prefer": "return=minimal"})
		if res.StatusCode != 201 || len(body) != 0 || !strings.HasPrefix(res.Header.Get("Location"), "/api/posts/") {
			t.Fatalf("minimal connect = %d %q Location=%q", res.StatusCode, body, res.Header.Get("Location"))
		}
		var p row
		getJSON(t, srv.URL+res.Header.Get("Location")+"?select=title,author_id", &p)
		if p["title"] != "M5N min" || int(p["author_id"].(float64)) != 1 {
			t.Fatalf("row = %v", p)
		}
	})
	t.Run("zero rows is 404 and inserts nothing", func(t *testing.T) {
		res, body := send(t, "POST", srv.URL+"/api/posts", `{"title":"M5N ghost","users":{"connect":{"id":999999}}}`, nil)
		wantProblem(t, res, body, 404, "not_found")
		res, body = send(t, "POST", srv.URL+"/api/posts", `{"title":"M5N ghost","users":{"connect":{"id":999999}}}`, map[string]string{"Prefer": "return=minimal"})
		wantProblem(t, res, body, 404, "not_found")
		var rows []row
		getJSON(t, srv.URL+"/api/posts?title=eq.M5N%20ghost", &rows)
		if len(rows) != 0 {
			t.Fatalf("ghost rows inserted: %v", rows)
		}
	})
	t.Run("both fk and nested", func(t *testing.T) {
		res, body := send(t, "POST", srv.URL+"/api/posts", `{"title":"M5N x","author_id":1,"users":{"connect":{"id":1}}}`, nil)
		wantProblem(t, res, body, 400, "invalid_body")
		res, body = send(t, "POST", srv.URL+"/api/posts", `{"title":"M5N x","users":{"connect":{"id":1},"create":{"email":"x"}}}`, nil)
		wantProblem(t, res, body, 400, "invalid_body")
	})
}

func TestNestedChildren(t *testing.T) {
	sweepNested(t)
	srv := testServer(t, nil)
	email := nestedEmail(t)
	var user row
	sendJSON(t, "POST", srv.URL+"/api/users?select=id,email,posts(id,title,body)",
		`{"email":"`+email+`","posts":{"create":[{"title":"M5N one"},{"title":"M5N two","body":"b"}]}}`, 201, &user)
	posts, _ := user["posts"].([]any)
	if len(posts) != 2 {
		t.Fatalf("response embed must show the new children (visibility CTE): %v", user)
	}
	titles := map[string]bool{}
	for _, p := range posts {
		titles[p.(map[string]any)["title"].(string)] = true
	}
	if !titles["M5N one"] || !titles["M5N two"] {
		t.Fatalf("children = %v", posts)
	}
	uid := rowID(t, user)
	var rows []row
	getJSON(t, srv.URL+fmt.Sprintf("/api/posts?author_id=eq.%d&select=id,author_id,title", uid), &rows)
	if len(rows) != 2 {
		t.Fatalf("children not persisted with the parent's key: %v", rows)
	}
	t.Run("nested in nested with embeds", func(t *testing.T) {
		var u row
		sendJSON(t, "POST", srv.URL+"/api/users?select=id,posts(id,title,comments(id,body,post_id))&posts.limit=5&posts.comments.limit=5",
			`{"email":"`+nestedEmail(t)+`","posts":{"create":[{"title":"M5N deep","comments":{"create":[{"body":"M5N c1"},{"body":"M5N c2"}]}}]}}`, 201, &u)
		ps, _ := u["posts"].([]any)
		if len(ps) != 1 {
			t.Fatalf("posts = %v", u)
		}
		cs, _ := ps[0].(map[string]any)["comments"].([]any)
		if len(cs) != 2 || cs[0].(map[string]any)["post_id"] != ps[0].(map[string]any)["id"] {
			t.Fatalf("grandchildren not visible in the response: %v", u)
		}
	})
	t.Run("child forward key overlapping the parent link", func(t *testing.T) {
		res, body := send(t, "POST", srv.URL+"/api/users",
			`{"email":"`+nestedEmail(t)+`","posts":{"create":[{"title":"M5N x","users":{"connect":{"id":1}}}]}}`, nil)
		wantProblem(t, res, body, 400, "invalid_body")
	})
	t.Run("depth", func(t *testing.T) {
		res, body := send(t, "POST", srv.URL+"/api/comments",
			`{"body":"M5N d","posts":{"create":{"title":"M5N d","users":{"create":{"email":"`+nestedEmail(t)+`","places":{"create":[{"name":"M5N d"}]}}}}}}`, nil)
		p := wantProblem(t, res, body, 400, "embed_depth_exceeded")
		if !strings.Contains(p.Detail, "max_depth 3") {
			t.Fatalf("detail = %q", p.Detail)
		}
		var c row
		sendJSON(t, "POST", srv.URL+"/api/comments?select=id,posts(id,users(id,email))",
			`{"body":"M5N d3","posts":{"create":{"title":"M5N d3","users":{"create":{"email":"`+nestedEmail(t)+`"}}}}}`, 201, &c)
		if c["posts"].(map[string]any)["users"].(map[string]any)["email"] == nil {
			t.Fatalf("depth-3 chain: %v", c)
		}
	})
}

func TestNestedAtomicRollback(t *testing.T) {
	sweepNested(t)
	for _, tc := range []struct {
		name string
		cfg  func(*config.Config)
	}{
		{"default (forced transaction)", nil},
		{"transactions.mutations off, force_transactions off", func(c *config.Config) {
			c.TX.Mutations = false
			c.Plugins.Settings.NestedMutations.ForceTransactions = false
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := testServer(t, tc.cfg)
			email := nestedEmail(t)
			okTitle := "M5N ok " + fmt.Sprint(nestedSeq)
			res, body := send(t, "POST", srv.URL+"/api/users", `{"email":"`+email+`","posts":{"create":[{"title":"`+okTitle+`"},{"title":null}]}}`, nil)
			p := wantProblem(t, res, body, 400, "pg:23502")
			if !strings.Contains(p.Detail, "title") {
				t.Fatalf("detail = %q", p.Detail)
			}
			res, body = get(t, srv.URL+"/api/users/by/email/"+email)
			if res.StatusCode != 404 {
				t.Fatalf("rollback leaked the parent row: %d %s", res.StatusCode, body)
			}
			var rows []row
			getJSON(t, srv.URL+"/api/posts?title=eq."+strings.ReplaceAll(okTitle, " ", "%20")+"&select=id", &rows)
			if len(rows) != 0 {
				t.Fatalf("rollback leaked a child row: %v", rows)
			}
			// The happy path still works without a forced transaction.
			var u row
			sendJSON(t, "POST", srv.URL+"/api/users?select=id,posts(id)", `{"email":"`+nestedEmail(t)+`","posts":{"create":[{"title":"M5N fine"}]}}`, 201, &u)
			if len(u["posts"].([]any)) != 1 {
				t.Fatalf("user = %v", u)
			}
		})
	}
}

func TestNestedPostGISColumn(t *testing.T) {
	sweepNested(t)
	srv := testServer(t, nil)
	var u row
	sendJSON(t, "POST", srv.URL+"/api/users?select=id,places(id,name,owner_id,location)",
		`{"email":"`+nestedEmail(t)+`","places":{"create":[{"name":"M5N geo","location":{"type":"Point","coordinates":[10.5,20.25]}},{"name":"M5N wkt","location":"SRID=4326;POINT(1 2)"}]}}`, 201, &u)
	places, _ := u["places"].([]any)
	if len(places) != 2 {
		t.Fatalf("places = %v", u)
	}
	byName := map[string]map[string]any{}
	for _, p := range places {
		m := p.(map[string]any)
		byName[m["name"].(string)] = m
		if m["owner_id"] != u["id"] {
			t.Fatalf("owner_id not filled from the parent: %v", m)
		}
	}
	loc, _ := byName["M5N geo"]["location"].(map[string]any)
	if loc == nil || loc["type"] != "Point" || fmt.Sprint(loc["coordinates"]) != "[10.5 20.25]" {
		t.Fatalf("GeoJSON round trip: %v", byName["M5N geo"])
	}
	loc, _ = byName["M5N wkt"]["location"].(map[string]any)
	if loc == nil || fmt.Sprint(loc["coordinates"]) != "[1 2]" {
		t.Fatalf("EWKT round trip: %v", byName["M5N wkt"])
	}
	res, body := send(t, "POST", srv.URL+"/api/users", `{"email":"`+nestedEmail(t)+`","places":{"create":[{"name":"M5N bad","location":"garbage"}]}}`, nil)
	wantProblem(t, res, body, 400, "invalid_body")
}

func TestNestedPluginDisabled(t *testing.T) {
	sweepNested(t)
	on := testServer(t, nil)
	off := testServer(t, func(c *config.Config) { c.Plugins.Disabled = []string{"nested_mutations"} })
	body := `{"email":"` + nestedEmail(t) + `","posts":{"create":[{"title":"M5N off"}]}}`
	res, out := send(t, "POST", off.URL+"/api/users", body, nil)
	p := wantProblem(t, res, out, 400, "invalid_body")
	if !strings.Contains(p.Detail, `unknown key "posts"`) {
		t.Fatalf("detail = %q", p.Detail)
	}
	res, out = send(t, "POST", off.URL+"/api/posts", `{"title":"M5N off","users":{"connect":{"id":1}}}`, nil)
	wantProblem(t, res, out, 400, "invalid_body")
	sendJSON(t, "POST", on.URL+"/api/users", body, 201, nil)

	schemas := func(srv string) map[string]json.RawMessage {
		_, doc := get(t, srv+"/api/openapi.json")
		var d struct {
			Components struct {
				Schemas map[string]json.RawMessage `json:"schemas"`
			} `json:"components"`
		}
		if err := json.Unmarshal(doc, &d); err != nil {
			t.Fatal(err)
		}
		return d.Components.Schemas
	}
	if _, ok := schemas(on.URL)["users.connect"]; !ok {
		t.Fatal("OpenAPI lacks users.connect with the plugin on")
	}
	if _, ok := schemas(off.URL)["users.connect"]; ok {
		t.Fatal("OpenAPI keeps users.connect with the plugin off")
	}
}
