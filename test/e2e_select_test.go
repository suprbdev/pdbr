package e2e

import (
	"net/url"
	"testing"

	"github.com/suprbdev/pdbr/internal/config"
)

func TestJSONPaths(t *testing.T) {
	srv := testServer(t, func(c *config.Config) { c.Server.Aggregates = true })
	base := srv.URL + "/api"
	q := func(kv ...string) string {
		v := url.Values{}
		for i := 0; i < len(kv); i += 2 {
			v.Add(kv[i], kv[i+1])
		}
		return v.Encode()
	}
	t.Run("select", func(t *testing.T) {
		res, body := get(t, base+"/users?"+q("select", `id,settings->theme,t:settings->>theme,tags->0,last:tags->>-1,address->>city,prev_addresses->0->>street,settings->missing->>x`, "order", "id"))
		if res.StatusCode != 200 {
			t.Fatalf("status %d: %s", res.StatusCode, body)
		}
		want := `[
			{"id":1,"theme":"dark","t":"dark","tags":"admin","last":"founder","city":"London","street":"1 Ockham Park","x":null},
			{"id":2,"theme":"light","t":"light","tags":"staff","last":"staff","city":null,"street":null,"x":null},
			{"id":3,"theme":null,"t":null,"tags":null,"last":null,"city":null,"street":null,"x":null}]`
		if !sameJSON(body, want) {
			t.Fatalf("body = %s", body)
		}
	})
	t.Run("filter", func(t *testing.T) {
		var rows []row
		getJSON(t, base+"/users?"+q("select", "id", "settings->>theme", "eq.dark"), &rows)
		if !eq(ids(rows, "id"), []int{1}) {
			t.Fatalf("->> eq: %v", rows)
		}
		getJSON(t, base+"/users?"+q("select", "id", "settings->theme", `in.("dark","light")`, "order", "id"), &rows)
		if !eq(ids(rows, "id"), []int{1, 2}) {
			t.Fatalf("-> in: %v", rows)
		}
		getJSON(t, base+"/users?"+q("select", "id", "settings", `cs.{"theme":"light"}`, "settings->>theme", "like.l*"), &rows)
		if !eq(ids(rows, "id"), []int{2}) {
			t.Fatalf("like: %v", rows)
		}
		getJSON(t, base+"/users?"+q("select", "id", "or", "(settings->>theme.eq.light,tags->>1.eq.founder)", "order", "id"), &rows)
		if !eq(ids(rows, "id"), []int{1, 2}) {
			t.Fatalf("logic: %v", rows)
		}
		getJSON(t, base+"/users?"+q("select", "id", "settings->theme", "is.null"), &rows)
		if !eq(ids(rows, "id"), []int{3}) {
			t.Fatalf("is.null: %v", rows)
		}
		getJSON(t, base+"/posts?"+q("select", "id,users(settings->>theme)", "users.settings->>theme", "eq.dark", "order", "id"), &rows)
		if len(rows) != 4 || rows[0]["users"].(row)["theme"] != "dark" || rows[2]["users"] != nil {
			t.Fatalf("embed filter: %v", rows)
		}
		res, body := get(t, base+"/users?"+q("settings->theme", "eq.dark"))
		wantProblem(t, res, body, 400, "invalid_filter")
		res, body = get(t, base+"/users?"+q("address->>city", "eq.London"))
		wantProblem(t, res, body, 400, "column_not_filterable")
	})
	t.Run("order and keyset", func(t *testing.T) {
		var rows []row
		res := getJSON(t, base+"/users?"+q("select", "id", "order", "settings->>theme.desc.nullsfirst", "limit", "2"), &rows)
		if !eq(ids(rows, "id"), []int{3, 2}) {
			t.Fatalf("order: %v", rows)
		}
		next := res.Header.Get("X-Next-Cursor")
		if next == "" {
			t.Fatal("no next cursor")
		}
		getJSON(t, base+"/users?"+q("select", "id", "order", "settings->>theme.desc.nullsfirst", "after", next), &rows)
		if !eq(ids(rows, "id"), []int{1}) {
			t.Fatalf("after: %v", rows)
		}
	})
	t.Run("casts", func(t *testing.T) {
		res, body := get(t, base+"/users?"+q("select", "id::text,balance::int8,mood::text,n:settings->>theme::text", "id", "eq.1"))
		if res.StatusCode != 200 || !sameJSON(body, `[{"id":"1","balance":0,"mood":"happy","n":"dark"}]`) {
			t.Fatalf("status %d: %s", res.StatusCode, body)
		}
		res, body = get(t, base+"/users?"+q("select", "id::regclass"))
		wantProblem(t, res, body, 400, "invalid_select")
	})
	t.Run("aggregates", func(t *testing.T) {
		res, body := get(t, base+"/users?"+q("select", "settings->>theme,count(),m:tags->>0.max()", "order", "settings->>theme.nullsfirst"))
		if res.StatusCode != 200 {
			t.Fatalf("status %d: %s", res.StatusCode, body)
		}
		want := `[{"theme":null,"count":1,"m":null},{"theme":"dark","count":1,"m":"admin"},{"theme":"light","count":1,"m":"staff"}]`
		if !sameJSON(body, want) {
			t.Fatalf("body = %s", body)
		}
		res, body = get(t, base+"/users?"+q("select", "mood::text,count()", "order", "mood"))
		wantProblem(t, res, body, 400, "column_not_orderable")
	})
	t.Run("csv", func(t *testing.T) {
		res, body := do(t, "GET", base+"/users?"+q("select", "id,settings->>theme", "id", "lt.3", "order", "id"), map[string]string{"Accept": "text/csv"})
		if res.StatusCode != 200 || string(body) != "id,theme\r\n1,dark\r\n2,light\r\n" {
			t.Fatalf("status %d: %q", res.StatusCode, body)
		}
	})
}

func TestMatchAndModifiers(t *testing.T) {
	srv := testServer(t, nil)
	base := srv.URL + "/api"
	cases := []struct {
		query string
		want  []int
	}{
		{"email=match.^a", []int{1, 3}},
		{"email=imatch.^GRACE", []int{2}},
		{"email=not.match.^a", []int{2}},
		{"email=like(any).{ada*,grace*}", []int{1, 2}},
		{"email=like(all).{a*,*lan*}", []int{3}},
		{"email=ilike(any).{ADA*}", []int{1}},
		{"email=match(all).{^a,lan}", []int{3}},
		{"email=imatch(any).{^ADA,^ALAN}", []int{1, 3}},
		{"id=eq(any).{1,3}", []int{1, 3}},
		{"id=gt(all).{1,2}", []int{3}},
		{"id=lte(any).{1}", []int{1}},
		{"mood=eq(any).{ok,sad}", []int{2}},
		{"email=not.eq(any).{ada@example.com}", []int{2, 3}},
		{"or=(email.match(any).{^g},id.eq(all).{3,3})", []int{2, 3}},
		{"settings->>theme=match(any).{^d,^l}", []int{1, 2}},
		{"id=eq(any).{}", nil},
		{"id=gt(all).{}", []int{1, 2, 3}},
	}
	for _, c := range cases {
		var rows []row
		getJSON(t, base+"/users?select=id&order=id&"+c.query, &rows)
		if got := ids(rows, "id"); !eq(got, c.want) {
			t.Errorf("%s: ids = %v, want %v", c.query, got, c.want)
		}
	}
	for _, q := range []string{"id=match.1", "email=neq(any).{a}", "tags=eq(any).{admin}", "email=eq(some).{a}", "id=eq(any).{x}"} {
		res, body := get(t, base+"/users?"+q)
		wantProblem(t, res, body, 400, "invalid_filter")
	}
	res, body := get(t, base+"/users?email=match.(")
	if res.StatusCode != 400 {
		t.Fatalf("bad regex: %d %s", res.StatusCode, body)
	}
}

func TestSpreadEmbeds(t *testing.T) {
	srv := testServer(t, func(c *config.Config) { c.Server.Aggregates = true })
	base := srv.URL + "/api"
	want := func(t *testing.T, path, body string) {
		t.Helper()
		res, got := get(t, base+path)
		if res.StatusCode != 200 || !sameJSON(got, body) {
			t.Fatalf("GET %s = %d %s", path, res.StatusCode, got)
		}
	}
	t.Run("to-one", func(t *testing.T) {
		want(t, "/posts?select=id,...users(author:email,mood)&order=id", `[
			{"id":1,"author":"ada@example.com","mood":"happy"},{"id":2,"author":"ada@example.com","mood":"happy"},
			{"id":3,"author":"grace@example.com","mood":"ok"},{"id":4,"author":"alan@example.com","mood":"happy"}]`)
		want(t, "/posts?select=id,...users(email)&users.mood=eq.ok&order=id&id=lt.4", `[{"id":1,"email":null},{"id":2,"email":null},{"id":3,"email":"grace@example.com"}]`)
		want(t, "/posts?select=id,...users!inner(email)&users.mood=eq.ok", `[{"id":3,"email":"grace@example.com"}]`)
	})
	t.Run("to-many", func(t *testing.T) {
		want(t, "/users?select=id,...posts(titles:title,ids:id)&posts.order=id.desc&order=id", `[
			{"id":1,"titles":["Unpublished draft","Notes on the Analytical Engine"],"ids":[2,1]},
			{"id":2,"titles":["Compilers 101"],"ids":[3]},
			{"id":3,"titles":["On Computable Numbers"],"ids":[4]}]`)
		want(t, "/users?select=id,...posts(ids:id)&posts.title=eq.none&id=eq.1", `[{"id":1,"ids":[]}]`)
	})
	t.Run("nested", func(t *testing.T) {
		want(t, "/places?select=name,...users(email,...posts(post_titles:title))&id=eq.3", `[{"name":"Greenwich","email":"grace@example.com","post_titles":["Compilers 101"]}]`)
		want(t, "/users?select=id,...places(place:name,...users(owner:email))&places.order=id&id=eq.2", `[{"id":2,"place":["Greenwich"],"owner":["grace@example.com"]}]`)
	})
	t.Run("aggregates", func(t *testing.T) {
		res, body := get(t, base+"/posts?select=n:count(),...users(mood)&count=true")
		if res.StatusCode != 200 || !sameJSON(body, `[{"n":1,"mood":"ok"},{"n":3,"mood":"happy"}]`) || res.Header.Get("X-Total-Count") != "2" {
			t.Fatalf("spread grouping = %d %s total=%q", res.StatusCode, body, res.Header.Get("X-Total-Count"))
		}
		want(t, "/posts?select=published,...users(newest:id.max(),n:count())", `[{"published":false,"newest":1,"n":1},{"published":true,"newest":3,"n":3}]`)
		want(t, "/users?select=id,posts(published,n:count(),...users(mood))&id=eq.1", `[{"id":1,"posts":[{"published":false,"n":1,"mood":"happy"},{"published":true,"n":1,"mood":"happy"}]}]`)
		for _, q := range []string{"/users?select=mood,count(),...posts(title)", "/users?select=id,...posts(id.count())", "/posts?select=count(),users(mood)", "/posts?select=id,u:...users(email)"} {
			res, body := get(t, base+q)
			wantProblem(t, res, body, 400, "invalid_select")
		}
	})
	t.Run("with computed arguments, paths, casts and modifiers", func(t *testing.T) {
		want(t, "/posts?select=id::text,excerpt(max_chars=5),...users(email,theme:settings->>theme)&title=imatch(any).{^notes,^x}",
			`[{"id":"1","excerpt":"First","email":"ada@example.com","theme":"dark"}]`)
	})
	t.Run("csv", func(t *testing.T) {
		res, body := do(t, "GET", base+"/posts?select=id,...users(email)&id=lt.3&order=id", map[string]string{"Accept": "text/csv"})
		if res.StatusCode != 200 || string(body) != "id,email\r\n1,ada@example.com\r\n2,ada@example.com\r\n" {
			t.Fatalf("csv = %d %q", res.StatusCode, body)
		}
		res, _ = do(t, "GET", base+"/posts?select=id,...users(email,places(id))", map[string]string{"Accept": "text/csv"})
		if res.StatusCode != 406 {
			t.Fatalf("csv with a nested embed = %d", res.StatusCode)
		}
	})
}
