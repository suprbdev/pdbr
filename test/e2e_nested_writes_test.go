package e2e

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbr/internal/config"
)

// nestedUser creates an m5n- user with the given nested body suffix
// through the API and returns the created row.
func nestedUser(t *testing.T, base, nested, sel string) row {
	t.Helper()
	var u row
	body := `{"email":"` + nestedEmail(t) + `"`
	if nested != "" {
		body += "," + nested
	}
	sendJSON(t, "POST", base+"/api/users?select="+sel, body+"}", 201, &u)
	return u
}

func nestedList(t *testing.T, r row, key string) []map[string]any {
	t.Helper()
	list, ok := r[key].([]any)
	if !ok {
		t.Fatalf("%s is not a list: %v", key, r)
	}
	out := make([]map[string]any, len(list))
	for i, x := range list {
		out[i] = x.(map[string]any)
	}
	return out
}

func idOf(m map[string]any) int { return int(m["id"].(float64)) }

func countRows(t *testing.T, query string) int {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, os.Getenv("PDBR_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var n int
	if err := conn.QueryRow(ctx, query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestNestedReverseConnect(t *testing.T) {
	sweepNested(t)
	srv := testServer(t, nil)
	a := nestedUser(t, srv.URL, `"posts":{"create":[{"title":"M5N moved"},{"title":"M5N stays"}]}`, "id,posts(id,title)")
	moved := idOf(nestedList(t, a, "posts")[0])

	var b row
	sendJSON(t, "POST", srv.URL+"/api/users?select=id,posts(id,title,author_id)",
		`{"email":"`+nestedEmail(t)+`","posts":{"connect":[{"id":`+fmt.Sprint(moved)+`}],"create":[{"title":"M5N new"}]}}`, 201, &b)
	posts := nestedList(t, b, "posts")
	if len(posts) != 2 {
		t.Fatalf("response must show the connected and the created post: %v", b)
	}
	for _, p := range posts {
		if int(p["author_id"].(float64)) != rowID(t, b) {
			t.Fatalf("post not linked to the new user: %v", p)
		}
	}
	var left row
	getJSON(t, srv.URL+fmt.Sprintf("/api/users/%d?select=id,posts(title)", rowID(t, a)), &left)
	if ps := nestedList(t, left, "posts"); len(ps) != 1 || ps[0]["title"] != "M5N stays" {
		t.Fatalf("connect did not move the post away from its old author: %v", left)
	}

	t.Run("missing row is 404 and rolls back", func(t *testing.T) {
		email := nestedEmail(t)
		res, body := send(t, "POST", srv.URL+"/api/users", `{"email":"`+email+`","posts":{"connect":[{"id":999999}]}}`, nil)
		p := wantProblem(t, res, body, 404, "not_found")
		if p.Detail != `nested "posts.connect[0]": matched 0 rows` {
			t.Fatalf("detail = %q", p.Detail)
		}
		if res, _ := get(t, srv.URL+"/api/users/by/email/"+email); res.StatusCode != 404 {
			t.Fatal("the parent row survived a failed connect")
		}
	})
}

func TestNestedPatchChildren(t *testing.T) {
	sweepNested(t)
	srv := testServer(t, nil)
	u := nestedUser(t, srv.URL, `"full_name":"Before","places":{"create":[{"name":"M5N keep"},{"name":"M5N drop"},{"name":"M5N loose"}]}`, "id,places(id,name)")
	uid := rowID(t, u)
	places := nestedList(t, u, "places")
	keep, drop, loose := idOf(places[0]), idOf(places[1]), idOf(places[2])
	other := nestedUser(t, srv.URL, `"places":{"create":[{"name":"M5N other"}]}`, "id,places(id)")
	otherPlace := idOf(nestedList(t, other, "places")[0])
	rowURL := fmt.Sprintf("%s/api/users/%d", srv.URL, uid)

	var got row
	sendJSON(t, "PATCH", rowURL+"?select=id,full_name,places(id,name)",
		fmt.Sprintf(`{"full_name":"After","places":{"update":[{"where":{"id":%d},"data":{"name":"M5N renamed"}}],"delete":[{"id":%d}],"disconnect":[{"id":%d}]}}`, keep, drop, loose), 200, &got)
	ps := nestedList(t, got, "places")
	if got["full_name"] != "After" || len(ps) != 1 || idOf(ps[0]) != keep || ps[0]["name"] != "M5N renamed" {
		t.Fatalf("response must show the table after the statement: %v", got)
	}
	if res, _ := get(t, fmt.Sprintf("%s/api/places/%d", srv.URL, drop)); res.StatusCode != 404 {
		t.Fatal("deleted place still exists")
	}
	var l row
	getJSON(t, fmt.Sprintf("%s/api/places/%d?select=owner_id", srv.URL, loose), &l)
	if l["owner_id"] != nil {
		t.Fatalf("disconnect left owner_id = %v", l["owner_id"])
	}

	t.Run("nested keys only lock the row", func(t *testing.T) {
		var r row
		sendJSON(t, "PATCH", rowURL+"?select=id,places(name)", fmt.Sprintf(`{"places":{"connect":[{"id":%d}],"create":[{"name":"M5N added"}]}}`, loose), 200, &r)
		names := map[string]bool{}
		for _, p := range nestedList(t, r, "places") {
			names[p["name"].(string)] = true
		}
		if len(names) != 3 || !names["M5N added"] || !names["M5N loose"] || !names["M5N renamed"] {
			t.Fatalf("places = %v", r)
		}
	})
	t.Run("missing child is 404 and rolls back", func(t *testing.T) {
		for _, body := range []string{
			`{"full_name":"Lost","places":{"update":[{"where":{"id":999999},"data":{"name":"x"}}]}}`,
			`{"full_name":"Lost","places":{"delete":[{"id":999999}]}}`,
			fmt.Sprintf(`{"full_name":"Lost","places":{"update":[{"where":{"id":%d},"data":{"name":"M5N stolen"}}]}}`, otherPlace),
			fmt.Sprintf(`{"full_name":"Lost","places":{"delete":[{"id":%d}]}}`, otherPlace),
		} {
			res, out := send(t, "PATCH", rowURL, body, nil)
			p := wantProblem(t, res, out, 404, "not_found")
			if !strings.HasSuffix(p.Detail, "[0]\": matched 0 rows") {
				t.Fatalf("detail = %q", p.Detail)
			}
		}
		var r row
		getJSON(t, rowURL+"?select=full_name", &r)
		if r["full_name"] != "After" {
			t.Fatalf("root patch survived a failed nested operation: %v", r)
		}
		var o row
		getJSON(t, fmt.Sprintf("%s/api/places/%d?select=name,owner_id", srv.URL, otherPlace), &o)
		if o["name"] != "M5N other" || int(o["owner_id"].(float64)) != rowID(t, other) {
			t.Fatalf("a row of another parent was touched: %v", o)
		}
	})
	t.Run("missing root is 404 and writes nothing", func(t *testing.T) {
		res, out := send(t, "PATCH", srv.URL+"/api/users/999999", `{"places":{"create":[{"name":"M5N orphan"}]}}`, nil)
		wantProblem(t, res, out, 404, "not_found")
		if n := countRows(t, `SELECT count(*) FROM places WHERE name = 'M5N orphan'`); n != 0 {
			t.Fatalf("%d orphans inserted", n)
		}
	})
	t.Run("database error rolls back the whole statement", func(t *testing.T) {
		res, out := send(t, "PATCH", rowURL, fmt.Sprintf(`{"full_name":"Broken","places":{"update":[{"where":{"id":%d},"data":{"name":"M5N gone"}}],"create":[{"name":null}]}}`, keep), nil)
		wantProblem(t, res, out, 400, "pg:23502")
		var r row
		getJSON(t, rowURL+"?select=full_name,places(name)", &r)
		if r["full_name"] != "After" {
			t.Fatalf("root patch leaked: %v", r)
		}
		for _, p := range nestedList(t, r, "places") {
			if p["name"] == "M5N gone" {
				t.Fatalf("child update leaked: %v", r)
			}
		}
	})
	t.Run("If-Match and bulk PATCH reject nested keys", func(t *testing.T) {
		res, out := send(t, "PATCH", rowURL, `{"places":{"create":[{"name":"M5N x"}]}}`, map[string]string{"If-Match": `W/"x"`})
		wantProblem(t, res, out, 400, "if_match_unsupported")
		res, out = send(t, "PATCH", fmt.Sprintf("%s/api/users?id=eq.%d", srv.URL, uid), `{"places":{"create":[{"name":"M5N x"}]}}`, nil)
		wantProblem(t, res, out, 400, "invalid_body")
	})
	t.Run("NOT NULL foreign key has no disconnect", func(t *testing.T) {
		res, out := send(t, "PATCH", rowURL, `{"posts":{"disconnect":[{"id":1}]}}`, nil)
		wantProblem(t, res, out, 400, "unknown_relation")
	})
}

func TestNestedPatchForward(t *testing.T) {
	sweepNested(t)
	srv := testServer(t, nil)
	u := nestedUser(t, srv.URL, `"full_name":"Owner","places":{"create":[{"name":"M5N fwd"}]}`, "id,places(id)")
	place := idOf(nestedList(t, u, "places")[0])
	placeURL := fmt.Sprintf("%s/api/places/%d", srv.URL, place)

	var got row
	sendJSON(t, "PATCH", placeURL+"?select=id,name,users(id,full_name)", `{"name":"M5N fwd2","users":{"update":{"full_name":"Renamed"}}}`, 200, &got)
	owner, _ := got["users"].(map[string]any)
	if got["name"] != "M5N fwd2" || owner == nil || owner["full_name"] != "Renamed" {
		t.Fatalf("forward update not visible in the response: %v", got)
	}

	sendJSON(t, "PATCH", placeURL+"?select=owner_id", `{"users":{"disconnect":true}}`, 200, &got)
	if got["owner_id"] != nil {
		t.Fatalf("disconnect: %v", got)
	}
	res, out := send(t, "PATCH", placeURL, `{"users":{"update":{"full_name":"x"}}}`, nil)
	if p := wantProblem(t, res, out, 404, "not_found"); p.Detail != `nested "users.update": matched 0 rows` {
		t.Fatalf("update of an unreferenced parent: %q", p.Detail)
	}

	sendJSON(t, "PATCH", placeURL+"?select=owner_id,users(email)", `{"users":{"connect":{"email":"grace@example.com"}}}`, 200, &got)
	if int(got["owner_id"].(float64)) != 2 || got["users"].(map[string]any)["email"] != "grace@example.com" {
		t.Fatalf("connect: %v", got)
	}
	res, out = send(t, "PATCH", placeURL, `{"name":"M5N nope","users":{"connect":{"email":"nobody@example.com"}}}`, nil)
	wantProblem(t, res, out, 404, "not_found")

	email := nestedEmail(t)
	sendJSON(t, "PATCH", placeURL+"?select=owner_id,users(email)", `{"users":{"create":{"email":"`+email+`"}}}`, 200, &got)
	if got["users"].(map[string]any)["email"] != email {
		t.Fatalf("create: %v", got)
	}

	t.Run("delete the referenced row", func(t *testing.T) {
		sendJSON(t, "PATCH", placeURL+"?select=owner_id,users(id)", `{"users":{"disconnect":true,"delete":true}}`, 200, &got)
		if got["owner_id"] != nil || got["users"] != nil {
			t.Fatalf("delete + disconnect: %v", got)
		}
		if res, _ := get(t, srv.URL+"/api/users/by/email/"+email); res.StatusCode != 404 {
			t.Fatal("referenced user not deleted")
		}
		owner := nestedUser(t, srv.URL, `"posts":{"create":[{"title":"M5N pin"}]},"places":{"create":[{"name":"M5N p2"}]}`, "id,email,places(id)")
		p2 := idOf(nestedList(t, owner, "places")[0])
		res, out := send(t, "PATCH", fmt.Sprintf("%s/api/places/%d", srv.URL, p2), `{"name":"M5N p2b","users":{"disconnect":true,"delete":true}}`, nil)
		wantProblem(t, res, out, 409, "pg:23503")
		var r row
		getJSON(t, fmt.Sprintf("%s/api/places/%d?select=name,owner_id", srv.URL, p2), &r)
		if r["name"] != "M5N p2" || int(r["owner_id"].(float64)) != rowID(t, owner) {
			t.Fatalf("failed delete leaked writes: %v", r)
		}
	})
	t.Run("validation", func(t *testing.T) {
		res, out := send(t, "PATCH", placeURL, `{"users":{"connect":{"id":1},"disconnect":true}}`, nil)
		wantProblem(t, res, out, 400, "invalid_body")
		res, out = send(t, "PATCH", srv.URL+"/api/posts/1", `{"users":{"disconnect":true}}`, nil)
		wantProblem(t, res, out, 400, "unknown_relation")
	})
}

func TestNestedPatchRLS(t *testing.T) {
	sweepNested(t)
	admin := testServer(t, nil)
	a := nestedUser(t, admin.URL, `"posts":{"create":[{"title":"M5N a1"}]}`, "id,posts(id)")
	b := nestedUser(t, admin.URL, `"posts":{"create":[{"title":"M5N b1"}]}`, "id,posts(id)")
	aid, bid := rowID(t, a), rowID(t, b)
	aPost, bPost := idOf(nestedList(t, a, "posts")[0]), idOf(nestedList(t, b, "posts")[0])
	srv := testServer(t, func(c *config.Config) {
		c.RLS.Enabled = true
		c.RLS.Auth.Mode = "headers"
	})
	as := func(uid int) map[string]string {
		return map[string]string{"X-Pdbr-Claim-Role": "app_user", "X-Pdbr-Claim-User-Id": fmt.Sprint(uid)}
	}
	aURL := fmt.Sprintf("%s/api/users/%d", srv.URL, aid)

	res, out := send(t, "PATCH", aURL+"?select=id,posts(title)", fmt.Sprintf(`{"posts":{"update":[{"where":{"id":%d},"data":{"title":"M5N a1 own"}}]}}`, aPost), as(aid))
	if res.StatusCode != 200 || !strings.Contains(string(out), "M5N a1 own") {
		t.Fatalf("own nested update = %d %s", res.StatusCode, out)
	}

	for name, body := range map[string]string{
		"connect another author's post": fmt.Sprintf(`{"full_name":"M5N hijack","posts":{"connect":[{"id":%d}]}}`, bPost),
		"update another author's post":  fmt.Sprintf(`{"full_name":"M5N hijack","posts":{"update":[{"where":{"id":%d},"data":{"title":"M5N stolen"}}]}}`, bPost),
	} {
		res, out := send(t, "PATCH", aURL, body, as(aid))
		if p := wantProblem(t, res, out, 404, "not_found"); !strings.Contains(p.Detail, "matched 0 rows") {
			t.Fatalf("%s: detail = %q", name, p.Detail)
		}
	}
	res, out = send(t, "PATCH", fmt.Sprintf("%s/api/users/%d", srv.URL, bid), fmt.Sprintf(`{"posts":{"delete":[{"id":%d}]}}`, bPost), as(aid))
	wantProblem(t, res, out, 404, "not_found")

	var check row
	getJSON(t, fmt.Sprintf("%s/api/posts/%d?select=title,author_id", admin.URL, bPost), &check)
	if check["title"] != "M5N b1" || int(check["author_id"].(float64)) != bid {
		t.Fatalf("RLS-hidden row was written: %v", check)
	}
	getJSON(t, fmt.Sprintf("%s/api/users/%d?select=full_name", admin.URL, aid), &check)
	if check["full_name"] != nil {
		t.Fatalf("root patch survived the denial: %v", check)
	}

	email := nestedEmail(t)
	res, out = send(t, "POST", srv.URL+"/api/users", `{"email":"`+email+`","posts":{"create":[{"title":"M5N check"}]}}`, as(aid))
	wantProblem(t, res, out, 403, "pg:42501")
	if res, _ := get(t, admin.URL+"/api/users/by/email/"+email); res.StatusCode != 404 {
		t.Fatal("WITH CHECK failure left the parent row behind")
	}
}

// nestedOmitFixture creates nw_owners / nw_items with smart comments
// gating nested operations per action.
func nestedOmitFixture(t *testing.T) {
	t.Helper()
	url := os.Getenv("PDBR_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	drop := func() {
		for _, ddl := range []string{`DROP TABLE IF EXISTS public.nw_items`, `DROP TABLE IF EXISTS public.nw_owners`} {
			if _, err := conn.Exec(ctx, ddl); err != nil {
				t.Errorf("nested omit fixture cleanup: %v", err)
			}
		}
	}
	drop()
	t.Cleanup(func() {
		drop()
		conn.Close(ctx)
	})
	for _, ddl := range []string{
		`CREATE TABLE public.nw_owners (
		    id     integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		    name   text NOT NULL,
		    secret text UNIQUE)`,
		`COMMENT ON COLUMN public.nw_owners.secret IS '@omit read'`,
		`CREATE TABLE public.nw_items (
		    id           integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		    owner_id     integer REFERENCES public.nw_owners (id),
		    owner_secret text REFERENCES public.nw_owners (secret),
		    label        text NOT NULL)`,
		`COMMENT ON TABLE public.nw_items IS '@omit delete'`,
		`COMMENT ON COLUMN public.nw_items.owner_id IS '@omit update'`,
		`COMMENT ON CONSTRAINT nw_items_owner_secret_fkey ON public.nw_items IS E'@fieldName secret_owner\n@foreignFieldName secret_items'`,
		`INSERT INTO public.nw_owners (name, secret) VALUES ('one', 's1')`,
		`INSERT INTO public.nw_items (owner_id, label) VALUES (1, 'i1')`,
	} {
		if _, err := conn.Exec(ctx, ddl); err != nil {
			t.Fatalf("nested omit fixture: %v\n%s", err, ddl)
		}
	}
}

func TestNestedOmitPerAction(t *testing.T) {
	nestedOmitFixture(t)
	srv := testServer(t, nil)
	var o row
	sendJSON(t, "PATCH", srv.URL+"/api/nw_owners/1?select=id,nw_items(label)", `{"nw_items":{"create":[{"label":"i2"}],"update":[{"where":{"id":1},"data":{"label":"i1b"}}]}}`, 200, &o)
	if len(nestedList(t, o, "nw_items")) != 2 {
		t.Fatalf("create + update: %v", o)
	}
	for _, body := range []string{
		`{"nw_items":{"delete":[{"id":1}]}}`,
		`{"nw_items":{"disconnect":[{"id":1}]}}`,
		`{"nw_items":{"connect":[{"id":1}]}}`,
	} {
		res, out := send(t, "PATCH", srv.URL+"/api/nw_owners/1", body, nil)
		wantProblem(t, res, out, 400, "unknown_relation")
	}
	res, out := send(t, "PATCH", srv.URL+"/api/nw_items/1", `{"nw_owners":{"connect":{"id":1}}}`, nil)
	wantProblem(t, res, out, 400, "unknown_relation")
	res, out = send(t, "POST", srv.URL+"/api/nw_owners", `{"name":"two","secret":"s2","secret_items":{"create":[{"label":"x"}]}}`, nil)
	if p := wantProblem(t, res, out, 400, "unknown_relation"); !strings.Contains(p.Detail, "hidden from reads") {
		t.Fatalf("detail = %q", p.Detail)
	}
	res, out = send(t, "POST", srv.URL+"/api/nw_items", `{"label":"x","secret_owner":{"connect":{"secret":"s1"}}}`, nil)
	wantProblem(t, res, out, 400, "unknown_relation")

	doc := openAPIDoc(t, srv.URL)
	props := func(schema, key string) map[string]any {
		s := schemaOf(doc, schema)
		p, _ := s["properties"].(map[string]any)[key].(map[string]any)
		if p == nil {
			return nil
		}
		ops, _ := p["properties"].(map[string]any)
		return ops
	}
	ops := props("nw_owners.update", "nw_items")
	if ops["create"] == nil || ops["update"] == nil || ops["delete"] != nil || ops["connect"] != nil || ops["disconnect"] != nil {
		t.Fatalf("nw_owners.update.nw_items ops = %v", ops)
	}
	if props("nw_owners.insert", "secret_items") != nil {
		t.Fatal("secret_items offered over a key hidden from reads")
	}
}
