package e2e

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbr/internal/config"
)

func dbInt(t *testing.T, sql string) int64 {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, os.Getenv("PDBR_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var n int64
	if err := conn.QueryRow(ctx, sql).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func wantRange(t *testing.T, method, url string, hdr map[string]string, wantRange, wantApplied string) {
	t.Helper()
	res, body := send(t, method, url, "", hdr)
	if res.StatusCode != 200 {
		t.Fatalf("%s %s = %d %s", method, url, res.StatusCode, body)
	}
	if got := res.Header.Get("Content-Range"); got != wantRange {
		t.Errorf("%s %s Content-Range = %q, want %q", method, url, got, wantRange)
	}
	if got := res.Header.Get("Preference-Applied"); got != wantApplied {
		t.Errorf("%s %s Preference-Applied = %q, want %q", method, url, got, wantApplied)
	}
}

func TestPreferCountContentRange(t *testing.T) {
	sweep(t)
	srv := testServer(t, nil)
	sendJSON(t, "POST", srv.URL+"/api/posts", `[{"author_id":1,"title":"M3 count a"},{"author_id":1,"title":"M3 count b"}]`, 201, nil)
	exact := dbInt(t, `SELECT count(*) FROM posts`)
	n := strconv.FormatInt(exact, 10)
	exactHdr := map[string]string{"Prefer": "count=exact"}

	wantRange(t, "GET", srv.URL+"/api/posts?limit=2", nil, "0-1/*", "")
	wantRange(t, "GET", srv.URL+"/api/posts?limit=2", exactHdr, "0-1/"+n, "count=exact")
	wantRange(t, "HEAD", srv.URL+"/api/posts?limit=2&offset=1", exactHdr, "1-2/"+n, "count=exact")
	wantRange(t, "GET", srv.URL+"/api/posts?limit=2&count=true", nil, "0-1/"+n, "")
	wantRange(t, "GET", srv.URL+fmt.Sprintf("/api/posts?offset=%d", exact+5), exactHdr, "*/"+n, "count=exact")
	res, _ := send(t, "GET", srv.URL+"/api/posts?limit=2", "", exactHdr)
	if res.Header.Get("X-Total-Count") != n {
		t.Errorf("X-Total-Count = %q, want %s", res.Header.Get("X-Total-Count"), n)
	}
	var page []row
	res = getJSON(t, srv.URL+"/api/posts?limit=1", &page)
	after := res.Header.Get("X-Next-Cursor")
	wantRange(t, "GET", srv.URL+"/api/posts?limit=1&after="+after, exactHdr, "", "count=exact")

	users := strconv.FormatInt(dbInt(t, `SELECT count(*) FROM users`), 10)
	planned := strconv.FormatInt(dbInt(t, `SELECT reltuples::bigint FROM pg_class WHERE oid = 'public.users'::regclass`), 10)
	if planned == "-1" {
		planned = users
	}
	wantRange(t, "GET", srv.URL+"/api/users?limit=2&select=id", map[string]string{"Prefer": "count=planned"}, "0-1/"+planned, "count=planned")
	wantRange(t, "GET", srv.URL+"/api/posts?limit=2&select=id", map[string]string{"Prefer": "count=planned"}, "0-1/"+n, "count=exact")
	wantRange(t, "GET", srv.URL+"/api/posts?limit=2&title=like.M3*", map[string]string{"Prefer": "count=planned"}, "0-1/2", "count=exact")
	wantRange(t, "GET", srv.URL+"/api/posts?limit=2", map[string]string{"Prefer": "count=estimated"}, "0-1/"+n, "count=exact")
	wantRange(t, "GET", srv.URL+"/api/metrics?limit=1", map[string]string{"Prefer": "count=planned"}, "0-0/"+strconv.FormatInt(dbInt(t, `SELECT count(*) FROM metrics`), 10), "count=exact")
	wantRange(t, "GET", srv.URL+"/api/rpc/search_posts?term=M3&limit=1", exactHdr, "0-0/2", "count=exact")

	small := testServer(t, func(c *config.Config) { c.Server.MaxPageSize = 2 })
	pl, _ := strconv.ParseInt(planned, 10, 64)
	est := strconv.FormatInt(max(pl, 3), 10)
	wantRange(t, "GET", small.URL+"/api/users?limit=1", map[string]string{"Prefer": "count=estimated"}, "0-0/"+est, "count=estimated")

	res, body := send(t, "PATCH", srv.URL+"/api/posts?title=like.M3%20count*&select=id", `{"body":"x"}`, map[string]string{"Prefer": "count=planned"})
	if res.StatusCode != 200 || res.Header.Get("Content-Range") != "0-1/2" || res.Header.Get("Preference-Applied") != "count=exact" {
		t.Fatalf("bulk PATCH count = %d %s %v", res.StatusCode, body, res.Header)
	}
	res, body = send(t, "DELETE", srv.URL+"/api/posts?title=like.M3%20count*", "", map[string]string{"Prefer": "return=minimal"})
	if res.StatusCode != 204 || res.Header.Get("Content-Range") != "*/*" {
		t.Fatalf("bulk DELETE minimal = %d %s %v", res.StatusCode, body, res.Header)
	}
}

func TestPlannedCountPartitionsAndInheritance(t *testing.T) {
	dbURL := os.Getenv("PDBR_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `DROP TABLE IF EXISTS e2e_part_probe, e2e_inh_child, e2e_inh_parent;
		CREATE TABLE e2e_part_probe (id int, k int) PARTITION BY RANGE (k);
		CREATE TABLE e2e_part_probe_a PARTITION OF e2e_part_probe FOR VALUES FROM (0) TO (10);
		CREATE TABLE e2e_part_probe_b PARTITION OF e2e_part_probe FOR VALUES FROM (10) TO (20);
		INSERT INTO e2e_part_probe SELECT g, g FROM generate_series(1, 15) g;
		CREATE TABLE e2e_inh_parent (id int);
		CREATE TABLE e2e_inh_child () INHERITS (e2e_inh_parent);
		INSERT INTO e2e_inh_parent VALUES (1);
		INSERT INTO e2e_inh_child SELECT generate_series(1, 4);`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), dbURL)
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		_, _ = c.Exec(context.Background(), `DROP TABLE IF EXISTS e2e_part_probe, e2e_inh_child, e2e_inh_parent`)
	})
	srv := testServer(t, nil)
	planned := map[string]string{"Prefer": "count=planned"}
	wantRange(t, "GET", srv.URL+"/api/e2e_part_probe?limit=1", planned, "0-0/15", "count=planned")
	if _, err := conn.Exec(ctx, `ANALYZE e2e_part_probe; ANALYZE e2e_inh_parent; ANALYZE e2e_inh_child;
		INSERT INTO e2e_part_probe SELECT g, g FROM generate_series(16, 18) g`); err != nil {
		t.Fatal(err)
	}
	wantRange(t, "GET", srv.URL+"/api/e2e_part_probe?limit=1", planned, "0-0/15", "count=planned")
	wantRange(t, "GET", srv.URL+"/api/e2e_part_probe?limit=1", map[string]string{"Prefer": "count=exact"}, "0-0/18", "count=exact")
	wantRange(t, "GET", srv.URL+"/api/e2e_inh_parent?limit=1", planned, "0-0/5", "count=planned")
}
