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
