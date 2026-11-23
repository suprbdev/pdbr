package e2e

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbr/internal/config"
)

func aggServer(t *testing.T, mutate func(*config.Config)) string {
	t.Helper()
	return testServer(t, func(c *config.Config) {
		c.Server.Aggregates = true
		if mutate != nil {
			mutate(c)
		}
	}).URL + "/api"
}

// aggRows GETs url and returns the body decoded as rows, failing on any
// status but 200.
func aggRows(t *testing.T, url string, hdr map[string]string) []row {
	t.Helper()
	res, body := do(t, "GET", url, hdr)
	if res.StatusCode != 200 {
		t.Fatalf("GET %s = %d %s", url, res.StatusCode, body)
	}
	var rows []row
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("GET %s: %s: %v", url, body, err)
	}
	return rows
}

func dbText(t *testing.T, sql string) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, os.Getenv("PDBR_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var s string
	if err := conn.QueryRow(ctx, sql).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func wantRows(t *testing.T, url string, want string) {
	t.Helper()
	got := aggRows(t, url, nil)
	var w []row
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, w) {
		gb, _ := json.Marshal(got)
		t.Errorf("GET %s\n got %s\nwant %s", url, gb, want)
	}
}

func TestAggregatesDisabled(t *testing.T) {
	url := testServer(t, nil).URL + "/api"
	for _, q := range []string{"/posts?select=count()", "/posts?select=author_id,id.sum()", "/users?select=id,posts(count())", "/places?select=location.extent()"} {
		res, body := get(t, url+q)
		wantProblem(t, res, body, 400, "aggregates_disabled")
	}
}

func TestAggregates(t *testing.T) {
	url := aggServer(t, nil)
	wantRows(t, url+"/posts?select=count()", `[{"count": 4}]`)
	wantRows(t, url+"/posts?select=author_id,count(),id.max()&order=author_id.desc",
		`[{"author_id": 3, "count": 1, "max": 4}, {"author_id": 2, "count": 1, "max": 3}, {"author_id": 1, "count": 2, "max": 2}]`)
	wantRows(t, url+"/posts?select=total:id::numeric.sum()::text,mean:id.avg()::int,low:id.min(),n:body.count()",
		`[{"total": "10", "mean": 3, "low": 1, "n": 4}]`)
	wantRows(t, url+"/posts?select=count()&author_id=eq.1", `[{"count": 2}]`)
	wantRows(t, url+"/posts?select=count()&author_id=eq.99", `[{"count": 0}]`)
	wantRows(t, url+"/posts?select=author_id,count()&author_id=eq.99", `[]`)
	wantRows(t, url+"/users?select=balance.sum(),created_at.count()", `[{"sum": 0, "count": 3}]`)
	wantRows(t, url+"/users?select=mood,count()&order=mood", `[{"mood": "ok", "count": 1}, {"mood": "happy", "count": 2}]`)

	wantRows(t, url+"/users?select=id,posts(count())&order=id",
		`[{"id": 1, "posts": [{"count": 2}]}, {"id": 2, "posts": [{"count": 1}]}, {"id": 3, "posts": [{"count": 1}]}]`)
	wantRows(t, url+"/users?select=id,posts(author_id,n:count(),id.max())&posts.order=author_id&id=eq.1",
		`[{"id": 1, "posts": [{"author_id": 1, "n": 2, "max": 2}]}]`)
	wantRows(t, url+"/users?select=id,posts(count)&id=lt.3",
		`[{"id": 1, "posts": [{"count": 2}]}, {"id": 2, "posts": [{"count": 1}]}]`)
	wantRows(t, url+"/users?select=mood.max()::mood,low:mood::text.min(),day:created_at::date.max()::text,n:count()&id=eq.1",
		`[{"max": "happy", "low": "happy", "day": "`+dbText(t, `SELECT created_at::date::text FROM users WHERE id = 1`)+`", "n": 1}]`)

	wantRows(t, url+"/rpc/search_posts?term=o&select=author_id,count()&order=author_id.desc",
		`[{"author_id": 3, "count": 1}, {"author_id": 2, "count": 1}, {"author_id": 1, "count": 1}]`)

	res, body := do(t, "GET", url+"/posts?select=author_id,count()&limit=2&offset=1", map[string]string{"Prefer": "count=planned"})
	if res.StatusCode != 206 || res.Header.Get("Content-Range") != "1-2/3" || res.Header.Get("Preference-Applied") != "count=exact" || res.Header.Get("X-Next-Cursor") != "" {
		t.Errorf("paged groups = %d %v %s", res.StatusCode, res.Header, body)
	}
	if !strings.Contains(res.Header.Get("Link"), "offset=0") {
		t.Errorf("Link = %q", res.Header.Get("Link"))
	}
	res, _ = do(t, "GET", url+"/posts?select=count()&count=true", nil)
	if res.Header.Get("X-Total-Count") != "1" {
		t.Errorf("ungrouped total = %q", res.Header.Get("X-Total-Count"))
	}
	res, _ = do(t, "GET", url+"/users?select=mood,post_count,count()&count=true&limit=1", nil)
	if res.Header.Get("X-Total-Count") != "3" {
		t.Errorf("total over a computed key = %q", res.Header.Get("X-Total-Count"))
	}

	res, body = do(t, "GET", url+"/posts?select=author_id,n:count()&order=author_id", map[string]string{"Accept": "text/csv"})
	if res.StatusCode != 200 || string(body) != "author_id,n\r\n1,2\r\n2,1\r\n3,1\r\n" {
		t.Errorf("csv = %d %q", res.StatusCode, body)
	}
	res, _ = do(t, "HEAD", url+"/posts?select=author_id,count()", nil)
	if res.StatusCode != 200 {
		t.Errorf("HEAD = %d", res.StatusCode)
	}
}

func TestAggregatesSpatial(t *testing.T) {
	url := aggServer(t, nil)
	rows := aggRows(t, url+"/places?select=owner_id,location.extent(),c:location.centroid(),u:location.union(),g:location.collect(),n:location.count()&owner_id=eq.1", nil)
	if len(rows) != 1 || rows[0]["n"] != float64(2) {
		t.Fatalf("rows = %v", rows)
	}
	geom := func(key, typ string) {
		t.Helper()
		g, ok := rows[0][key].(map[string]any)
		if !ok || g["type"] != typ {
			t.Errorf("%s = %v, want a GeoJSON %s", key, rows[0][key], typ)
		}
	}
	geom("extent", "Polygon")
	geom("c", "Point")
	geom("u", "MultiPoint")
	geom("g", "MultiPoint")
	c := rows[0]["c"].(map[string]any)["coordinates"].([]any)
	if x := c[0].(float64); x < -0.1281 || x > -0.1270 {
		t.Errorf("centroid = %v", c)
	}
	rows = aggRows(t, url+"/places?select=area.extent(),area.count()", nil)
	if g, _ := rows[0]["extent"].(map[string]any); g["type"] != "Polygon" || rows[0]["count"] != float64(1) {
		t.Errorf("geography extent = %v", rows)
	}
	rows = aggRows(t, url+"/places?select=location.extent()::text", nil)
	if s, _ := rows[0]["extent"].(string); s == "" {
		t.Errorf("extent::text = %v", rows)
	}
	off := aggServer(t, func(c *config.Config) {
		c.Plugins.Settings["postgis"] = map[string]any{"aggregates": false}
	})
	res, body := get(t, off+"/places?select=location.extent()")
	wantProblem(t, res, body, 400, "invalid_select")
	wantRows(t, off+"/places?select=location.count()", `[{"count": 4}]`)
}

func TestAggregatesRLS(t *testing.T) {
	url := aggServer(t, func(c *config.Config) {
		c.RLS.Enabled = true
		c.RLS.Auth.Mode = "headers"
	})
	if rows := aggRows(t, url+"/posts?select=count()", nil); rows[0]["count"] != float64(3) {
		t.Errorf("anonymous count = %v", rows)
	}
	owner := map[string]string{"X-Pdbr-Claim-Role": "app_user", "X-Pdbr-Claim-User-Id": "1"}
	if rows := aggRows(t, url+"/posts?select=count()", owner); rows[0]["count"] != float64(4) {
		t.Errorf("owner count = %v", rows)
	}
	if rows := aggRows(t, url+"/users?select=id,posts(count())&id=eq.1", nil); !reflect.DeepEqual(rows[0]["posts"], []any{map[string]any{"count": float64(1)}}) {
		t.Errorf("anonymous embedded count = %v", rows)
	}
}

func TestAggregatesErrors(t *testing.T) {
	url := aggServer(t, func(c *config.Config) { c.Server.MaxCost = 1000 })
	cases := []struct {
		q    string
		code string
	}{
		{"/posts?select=author_id,count()&after=WyJwdWJsaWMucG9zdHMiLDFd", "keyset_unsupported"},
		{"/posts?select=author_id,count()&order=id", "column_not_orderable"},
		{"/posts?select=author_id,count()&distinct=author_id", "invalid_select"},
		{"/posts?select=id,users(count())", "invalid_select"},
		{"/posts/1?select=count()", "invalid_select"},
		{"/posts?select=title.sum()", "invalid_select"},
		{"/posts?select=id.sum(),author_id.sum()", "invalid_select"},
		{"/posts?select=id::regclass", "invalid_select"},
		{"/places?select=location.max()", "invalid_select"},
		{"/posts?select=id::nosuchtype.sum()", "invalid_select"},
		{"/posts?select=id.count(),users(mood)", "invalid_select"},
		{"/users?select=count(),posts!inner(id)", "invalid_select"},
		{"/users?select=tag_words,count()", "invalid_select"},
		{"/users?select=id,posts(count())", "cost_exceeded"},
	}
	for _, typ := range []string{"regclass", "regrole", "regproc", "regprocedure", "regoper", "regoperator", "regnamespace", "regtype", "regconfig", "regdictionary", "regcollation", "oid", "record", "users", "address", "event_type"} {
		cases = append(cases, struct {
			q    string
			code string
		}{"/posts?select=id.max()::" + typ, "invalid_select"}, struct {
			q    string
			code string
		}{"/posts?select=id::" + typ + ".max()", "invalid_select"})
	}
	for _, c := range cases {
		res, body := get(t, url+c.q)
		wantProblem(t, res, body, 400, c.code)
	}
	wantRows(t, url+"/users?select=id,posts(count())&limit=5&id=eq.2", `[{"id": 2, "posts": [{"count": 1}]}]`)
}
