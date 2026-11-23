package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbr/internal/config"
)

type partialReport struct {
	N      int `json:"n"`
	Failed int `json:"failed"`
	Items  []struct {
		Index  int             `json:"index"`
		Status int             `json:"status"`
		Row    json.RawMessage `json:"row"`
		Code   string          `json:"code"`
		Detail string          `json:"detail"`
	} `json:"items"`
}

func partialServer(t *testing.T, mutate func(*config.Config)) string {
	t.Helper()
	return testServer(t, func(c *config.Config) {
		c.Server.PartialBulkWrites = true
		if mutate != nil {
			mutate(c)
		}
	}).URL
}

func partialPost(t *testing.T, base, path, body string, status int, hdr map[string]string) (partialReport, http.Header) {
	t.Helper()
	h := map[string]string{"Prefer": "partial=items"}
	for k, v := range hdr {
		h[k] = v
	}
	res, out := send(t, "POST", base+path, body, h)
	if res.StatusCode != status {
		t.Fatalf("POST %s = %d %s, want %d", path, res.StatusCode, out, status)
	}
	var rep partialReport
	if err := json.Unmarshal(out, &rep); err != nil {
		t.Fatalf("bad report %s: %v", out, err)
	}
	return rep, res.Header
}

func countUsers(t *testing.T, base, like string) int {
	t.Helper()
	var rows []row
	getJSON(t, base+"/api/users?select=id&email=like."+url.QueryEscape(like), &rows)
	return len(rows)
}

func TestPartialBulkInsert(t *testing.T) {
	sweep(t)
	base := partialServer(t, nil)
	prefix := fmt.Sprintf("m3-partial-%d", time.Now().UnixNano())
	a, c := prefix+"-a@example.com", prefix+"-c@example.com"
	body := fmt.Sprintf(`[{"email":%q},{"email":%q},{"email":%q}]`, a, a, c)

	t.Run("one bad element, the others commit", func(t *testing.T) {
		rep, hdr := partialPost(t, base, "/api/users", body, 207, nil)
		if rep.N != 2 || rep.Failed != 1 || len(rep.Items) != 3 {
			t.Fatalf("report = %+v", rep)
		}
		if rep.Items[0].Status != 201 || rep.Items[2].Status != 201 || len(rep.Items[0].Row) == 0 {
			t.Fatalf("items = %+v", rep.Items)
		}
		if it := rep.Items[1]; it.Status != 409 || it.Code != "pg:23505" || it.Row != nil || it.Index != 1 {
			t.Fatalf("failed item = %+v", it)
		}
		if hdr.Get("Preference-Applied") != "partial=items" {
			t.Fatalf("Preference-Applied = %q", hdr.Get("Preference-Applied"))
		}
		if n := countUsers(t, base, prefix+"-*"); n != 2 {
			t.Fatalf("committed %d users, want 2", n)
		}
	})

	t.Run("all elements succeed", func(t *testing.T) {
		e1, e2 := prefix+"-d@example.com", prefix+"-e@example.com"
		rep, _ := partialPost(t, base, "/api/users", fmt.Sprintf(`[{"email":%q},{"email":%q}]`, e1, e2), 201, nil)
		if rep.N != 2 || rep.Failed != 0 {
			t.Fatalf("report = %+v", rep)
		}
	})

	t.Run("return=minimal omits rows", func(t *testing.T) {
		e1, e2 := prefix+"-f@example.com", prefix+"-f@example.com"
		rep, hdr := partialPost(t, base, "/api/users", fmt.Sprintf(`[{"email":%q},{"email":%q}]`, e1, e2), 207, map[string]string{"Prefer": "return=minimal, partial=items"})
		if rep.N != 1 || rep.Items[0].Row != nil || rep.Items[0].Status != 201 || rep.Items[1].Status != 409 {
			t.Fatalf("report = %+v", rep)
		}
		if hdr.Get("Preference-Applied") != "return=minimal, partial=items" {
			t.Fatalf("Preference-Applied = %q", hdr.Get("Preference-Applied"))
		}
	})

	t.Run("ignore-duplicates skips an element without failing it", func(t *testing.T) {
		rep, _ := partialPost(t, base, "/api/users?on_conflict=email", fmt.Sprintf(`[{"email":%q},{"email":%q}]`, a, prefix+"-g@example.com"), 201,
			map[string]string{"Prefer": "resolution=ignore-duplicates, partial=items"})
		if rep.N != 2 || rep.Items[0].Status != 204 || rep.Items[0].Row != nil || rep.Items[1].Status != 201 {
			t.Fatalf("report = %+v", rep)
		}
	})

	t.Run("PUT upserts each element", func(t *testing.T) {
		res, out := send(t, "PUT", base+"/api/users?on_conflict=email",
			fmt.Sprintf(`[{"email":%q,"full_name":"Upserted"},{"email":%q,"full_name":"Upserted"}]`, a, prefix+"-h@example.com"),
			map[string]string{"Prefer": "partial=items"})
		var rep partialReport
		if err := json.Unmarshal(out, &rep); err != nil {
			t.Fatalf("%s: %v", out, err)
		}
		if res.StatusCode != 200 || rep.N != 2 || rep.Failed != 0 || rep.Items[0].Status != 200 {
			t.Fatalf("PUT = %d %s", res.StatusCode, out)
		}
	})

	t.Run("default is unchanged: all or nothing", func(t *testing.T) {
		x := prefix + "-aon@example.com"
		res, out := send(t, "POST", base+"/api/users", fmt.Sprintf(`[{"email":%q},{"email":%q}]`, x, x), nil)
		wantProblem(t, res, out, 409, "pg:23505")
		if n := countUsers(t, base, x); n != 0 {
			t.Fatalf("%d rows survived the rollback", n)
		}
	})

	t.Run("body validation still rejects the whole request", func(t *testing.T) {
		res, out := send(t, "POST", base+"/api/users", fmt.Sprintf(`[{"email":%q},{"nope":1}]`, prefix+"-v@example.com"), map[string]string{"Prefer": "partial=items"})
		wantProblem(t, res, out, 400, "invalid_body")
		if n := countUsers(t, base, prefix+"-v@*"); n != 0 {
			t.Fatal("a rejected body wrote")
		}
	})

	t.Run("a single object is not bulk", func(t *testing.T) {
		x := prefix + "-single@example.com"
		res, out := send(t, "POST", base+"/api/users", fmt.Sprintf(`{"email":%q}`, x), map[string]string{"Prefer": "partial=items"})
		if res.StatusCode != 201 || res.Header.Get("Preference-Applied") != "" {
			t.Fatalf("single POST = %d %s %v", res.StatusCode, out, res.Header)
		}
	})
}

func TestPartialBulkConfigOff(t *testing.T) {
	sweep(t)
	srv := testServer(t, nil)
	x := fmt.Sprintf("m3-partial-off-%d@example.com", time.Now().UnixNano())
	res, out := send(t, "POST", srv.URL+"/api/users", fmt.Sprintf(`[{"email":%q},{"email":%q}]`, x, x), map[string]string{"Prefer": "partial=items"})
	wantProblem(t, res, out, 409, "pg:23505")
	res, out = send(t, "POST", srv.URL+"/api/users", fmt.Sprintf(`[{"email":%q}]`, x), map[string]string{"Prefer": "handling=strict, partial=items"})
	wantProblem(t, res, out, 400, "invalid_preference")
}

func TestPartialBulkRollback(t *testing.T) {
	sweep(t)
	base := partialServer(t, func(c *config.Config) { c.Server.TxEnd = "commit-allow-override" })
	prefix := fmt.Sprintf("m3-partial-rb-%d", time.Now().UnixNano())
	body := fmt.Sprintf(`[{"email":"%[1]s-a@example.com"},{"email":"%[1]s-a@example.com"},{"email":"%[1]s-b@example.com"}]`, prefix)
	rep, hdr := partialPost(t, base, "/api/users", body, 207, map[string]string{"Prefer": "tx=rollback, partial=items"})
	if rep.N != 2 || rep.Failed != 1 || hdr.Get("Preference-Applied") != "tx=rollback, partial=items" {
		t.Fatalf("report = %+v %v", rep, hdr)
	}
	if n := countUsers(t, base, prefix+"-*"); n != 0 {
		t.Fatalf("tx=rollback left %d rows", n)
	}
}

func TestPartialBulkRLS(t *testing.T) {
	url := os.Getenv("PDBR_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set")
	}
	title := fmt.Sprintf("M3 partial rls %d", time.Now().UnixNano())
	sweep(t)
	base := partialServer(t, func(c *config.Config) {
		c.RLS.Enabled = true
		c.RLS.Auth.Mode = "headers"
	})
	hdr := map[string]string{"X-Pdbr-Claim-Role": "app_user", "X-Pdbr-Claim-User-Id": "1"}
	body := fmt.Sprintf(`[{"author_id":1,"title":%q},{"author_id":2,"title":%q},{"author_id":1,"title":%q}]`, title+" a", title+" b", title+" c")
	rep, _ := partialPost(t, base, "/api/posts", body, 207, hdr)
	if rep.N != 2 || rep.Failed != 1 || rep.Items[1].Status != 403 || rep.Items[1].Code != "pg:42501" {
		t.Fatalf("report = %+v", rep)
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM posts WHERE title LIKE $1`, title+"%").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("committed %d posts, want 2", n)
	}
}

func TestPartialBulkSerializationRetry(t *testing.T) {
	url := os.Getenv("PDBR_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set")
	}
	sweep(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	for _, q := range []string{
		`CREATE SEQUENCE IF NOT EXISTS m3_partial_probe_seq`,
		`CREATE OR REPLACE FUNCTION m3_partial_probe() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.email LIKE 'm3-partial-ser-always-%' OR (NEW.email LIKE 'm3-partial-ser-once-%' AND nextval('m3_partial_probe_seq') % 2 = 1) THEN
    RAISE EXCEPTION 'could not serialize access' USING ERRCODE = '40001';
  END IF;
  RETURN NEW;
END $$`,
		`DROP TRIGGER IF EXISTS m3_partial_probe ON users`,
		`CREATE TRIGGER m3_partial_probe BEFORE INSERT ON users FOR EACH ROW EXECUTE FUNCTION m3_partial_probe()`,
	} {
		if _, err := conn.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(ctx, url)
		if err != nil {
			return
		}
		defer c.Close(ctx)
		for _, q := range []string{`DROP TRIGGER IF EXISTS m3_partial_probe ON users`, `DROP FUNCTION IF EXISTS m3_partial_probe()`, `DROP SEQUENCE IF EXISTS m3_partial_probe_seq`} {
			_, _ = c.Exec(ctx, q)
		}
	})
	stamp := time.Now().UnixNano()
	mk := func(kind string) string {
		return fmt.Sprintf(`[{"email":"m3-partial-ok-%d@example.com"},{"email":"m3-partial-ser-%s-%d@example.com"},{"email":"m3-partial-ok2-%d@example.com"}]`, stamp, kind, stamp, stamp)
	}

	retry := partialServer(t, func(c *config.Config) { c.TX.MaxRetries = 1 })
	var cur int64
	for {
		if err := conn.QueryRow(ctx, `SELECT nextval('m3_partial_probe_seq')`).Scan(&cur); err != nil {
			t.Fatal(err)
		}
		if cur%2 == 0 {
			break
		}
	}
	rep, _ := partialPost(t, retry, "/api/users", mk("once"), 201, nil)
	if rep.N != 3 || rep.Failed != 0 {
		t.Fatalf("retried request = %+v", rep)
	}
	if n := countUsers(t, retry, fmt.Sprintf("m3-partial-%%-%d@example.com", stamp)); n != 3 {
		t.Fatalf("the retry committed %d users, want 3 (once each)", n)
	}

	res, out := send(t, "POST", retry+"/api/users", mk("always"), map[string]string{"Prefer": "partial=items"})
	wantProblem(t, res, out, 409, "pg:40001")
	if n := countUsers(t, retry, fmt.Sprintf("m3-partial-ok%%-%d@example.com", stamp)); n != 2 {
		t.Fatalf("an exhausted retry changed the committed rows: %d, want the 2 from the first request", n)
	}
}
