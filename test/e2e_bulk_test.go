package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbr/internal/config"
)

// bulkUsers creates n users sharing an email prefix and returns the prefix
// (a like. filter selects exactly them).
func bulkUsers(t *testing.T, base string, n int) string {
	t.Helper()
	prefix := fmt.Sprintf("m3-bulk-%d", time.Now().UnixNano())
	body := "["
	for i := range n {
		if i > 0 {
			body += ","
		}
		body += fmt.Sprintf(`{"email":"%s-%d@example.com","mood":"sad"}`, prefix, i)
	}
	sendJSON(t, "POST", base+"/api/users", body+"]", 201, nil)
	return prefix
}

func byPrefix(prefix string) string { return "email=like." + url.QueryEscape(prefix+"-*") }

func TestBulkPatchDelete(t *testing.T) {
	sweep(t)
	srv := testServer(t, nil)
	prefix := bulkUsers(t, srv.URL, 3)
	sel := byPrefix(prefix)

	var rows []row
	res := sendJSON(t, "PATCH", srv.URL+"/api/users?"+sel+"&select=email,full_name", `{"full_name":"Bulk"}`, 200, &rows)
	if len(rows) != 3 || rows[0]["full_name"] != "Bulk" || len(rows[0]) != 2 || res.Header.Get("Location") != "" {
		t.Fatalf("bulk PATCH = %v", rows)
	}
	rows = nil
	sendJSON(t, "PATCH", srv.URL+"/api/users?"+sel+"&mood=eq.happy", `{"full_name":"none"}`, 200, &rows)
	if len(rows) != 0 {
		t.Fatalf("no match must be 200 [], got %v", rows)
	}
	res, body := send(t, "PATCH", srv.URL+"/api/users?"+sel, `{"mood":"ok"}`, map[string]string{"Prefer": "return=minimal"})
	if res.StatusCode != 204 || len(body) != 0 || res.Header.Get("Preference-Applied") != "return=minimal" {
		t.Fatalf("minimal = %d %q %v", res.StatusCode, body, res.Header)
	}
	rows = nil
	getJSON(t, srv.URL+"/api/users?"+sel+"&mood=eq.ok&select=id", &rows)
	if len(rows) != 3 {
		t.Fatalf("minimal PATCH not applied: %v", rows)
	}

	res, body = send(t, "DELETE", srv.URL+"/api/users", "", nil)
	wantProblem(t, res, body, 400, "unfiltered_mutation")
	res, body = send(t, "PATCH", srv.URL+"/api/users?select=id", `{"mood":"ok"}`, nil)
	wantProblem(t, res, body, 400, "unfiltered_mutation")
	res, body = send(t, "PATCH", srv.URL+"/api/users?full_name=eq.Bulk", `{"mood":"ok"}`, nil)
	wantProblem(t, res, body, 400, "column_not_filterable")
	res, body = send(t, "DELETE", srv.URL+"/api/users?"+sel+"&limit=1", "", nil)
	wantProblem(t, res, body, 400, "invalid_filter")

	email := prefix + "-0@example.com"
	var author []row
	getJSON(t, srv.URL+"/api/users?email=eq."+url.QueryEscape(email)+"&select=id", &author)
	if len(author) != 1 {
		t.Fatalf("author = %v", author)
	}
	sendJSON(t, "POST", srv.URL+"/api/posts", fmt.Sprintf(`[{"author_id":%d,"title":"M3 bulk a"},{"author_id":%d,"title":"M3 bulk b"}]`, rowID(t, author[0]), rowID(t, author[0])), 201, nil)
	rows = nil
	res, body = send(t, "DELETE", srv.URL+"/api/posts?users.email=eq."+url.QueryEscape(email)+"&select=title,users(email)", "", nil)
	wantProblem(t, res, body, 400, "unfiltered_mutation")
	sendJSON(t, "DELETE", srv.URL+"/api/posts?users.email=eq."+url.QueryEscape(email)+"&select=title,author:users(email)", "", 200, &rows)
	if len(rows) != 2 || rows[0]["author"].(map[string]any)["email"] != email {
		t.Fatalf("relation-filtered DELETE = %v", rows)
	}

	rows = nil
	res, body = send(t, "DELETE", srv.URL+"/api/users?"+sel, "", map[string]string{"Prefer": "return=headers-only"})
	if res.StatusCode != 204 || len(body) != 0 || res.Header.Get("Preference-Applied") != "return=headers-only" {
		t.Fatalf("headers-only DELETE = %d %q", res.StatusCode, body)
	}
	getJSON(t, srv.URL+"/api/users?"+sel, &rows)
	if len(rows) != 0 {
		t.Fatalf("rows survived: %v", rows)
	}
}

func TestBulkMaxAffected(t *testing.T) {
	sweep(t)
	for _, txOn := range []bool{true, false} {
		t.Run(fmt.Sprintf("transactions.mutations=%v", txOn), func(t *testing.T) {
			srv := testServer(t, func(c *config.Config) { c.TX.Mutations = txOn })
			prefix := bulkUsers(t, srv.URL, 3)
			sel := byPrefix(prefix)
			strict := func(n int) map[string]string {
				return map[string]string{"Prefer": fmt.Sprintf("handling=strict, max-affected=%d", n)}
			}
			res, body := send(t, "PATCH", srv.URL+"/api/users?"+sel, `{"full_name":"capped"}`, strict(2))
			if p := wantProblem(t, res, body, 400, "max_affected_exceeded"); p.Detail == "" {
				t.Error("no detail")
			}
			var rows []row
			getJSON(t, srv.URL+"/api/users?"+sel+"&select=full_name", &rows)
			for _, r := range rows {
				if r["full_name"] == "capped" {
					t.Fatalf("exceeded PATCH was not rolled back: %v", rows)
				}
			}
			res, body = send(t, "DELETE", srv.URL+"/api/users?"+sel, "", strict(1))
			wantProblem(t, res, body, 400, "max_affected_exceeded")
			rows = nil
			getJSON(t, srv.URL+"/api/users?"+sel, &rows)
			if len(rows) != 3 {
				t.Fatalf("exceeded DELETE was not rolled back: %d rows", len(rows))
			}
			res, body = send(t, "PATCH", srv.URL+"/api/users?"+sel, `{"full_name":"lenient"}`, map[string]string{"Prefer": "max-affected=1"})
			if res.StatusCode != 200 || res.Header.Get("Preference-Applied") != "" {
				t.Fatalf("lenient = %d %s %v", res.StatusCode, body, res.Header)
			}
			id := rowID(t, rows[0])
			res, body = send(t, "PATCH", fmt.Sprintf("%s/api/users/%d", srv.URL, id), `{"full_name":"zero"}`, strict(0))
			wantProblem(t, res, body, 400, "max_affected_exceeded")
			res, body = send(t, "DELETE", srv.URL+"/api/users?"+sel, "", strict(3))
			if res.StatusCode != 200 || res.Header.Get("Preference-Applied") != "handling=strict, max-affected=3" {
				t.Fatalf("within cap = %d %s %v", res.StatusCode, body, res.Header)
			}
		})
	}
	srv := testServer(t, nil)
	res, body := send(t, "PATCH", srv.URL+"/api/users/1", `{"full_name":"x"}`, map[string]string{"Prefer": "handling=strict, tx=rollback"})
	wantProblem(t, res, body, 400, "invalid_preference")
	res, body = send(t, "PATCH", srv.URL+"/api/users/1", `{"full_name":"Ada Lovelace"}`, map[string]string{"Prefer": "tx=rollback, return=minimal"})
	if res.StatusCode != 204 || res.Header.Get("Preference-Applied") != "return=minimal" {
		t.Fatalf("lenient unknown token = %d %s %v", res.StatusCode, body, res.Header)
	}
}

func TestBulkRLSAndUnfilteredKnob(t *testing.T) {
	sweep(t)
	dbURL := os.Getenv("PDBR_TEST_DATABASE_URL")
	admin := testServer(t, nil)
	sendJSON(t, "POST", admin.URL+"/api/posts", `[{"author_id":1,"title":"M3 bulk rls 1"},{"author_id":2,"title":"M3 bulk rls 2"}]`, 201, nil)
	rls := testServer(t, func(c *config.Config) {
		c.RLS.Enabled = true
		c.RLS.Auth.Mode = "headers"
	})
	var rows []row
	res, body := send(t, "PATCH", rls.URL+"/api/posts?title=like.M3%20bulk%20rls*&select=author_id", `{"body":"rls"}`,
		map[string]string{"X-Pdbr-Claim-Role": "app_user", "X-Pdbr-Claim-User-Id": "1"})
	if res.StatusCode != 200 {
		t.Fatalf("RLS bulk PATCH = %d %s", res.StatusCode, body)
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["author_id"] != float64(1) {
		t.Fatalf("RLS must scope the bulk write to own rows: %v", rows)
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `DROP TABLE IF EXISTS e2e_bulk_probe; CREATE TABLE e2e_bulk_probe (id serial PRIMARY KEY, label text);
		INSERT INTO e2e_bulk_probe (label) VALUES ('a'), ('b'), ('c')`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), dbURL)
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		_, _ = c.Exec(context.Background(), `DROP TABLE IF EXISTS e2e_bulk_probe`)
	})
	guarded := testServer(t, nil)
	res, body = send(t, "DELETE", guarded.URL+"/api/e2e_bulk_probe", "", nil)
	wantProblem(t, res, body, 400, "unfiltered_mutation")
	open := testServer(t, func(c *config.Config) { c.Server.AllowUnfilteredMutations = true })
	res, body = send(t, "PATCH", open.URL+"/api/e2e_bulk_probe", `{"label":"x"}`, map[string]string{"Prefer": "handling=strict, max-affected=2"})
	wantProblem(t, res, body, 400, "max_affected_exceeded")
	rows = nil
	sendJSON(t, "PATCH", open.URL+"/api/e2e_bulk_probe?select=label", `{"label":"x"}`, 200, &rows)
	if len(rows) != 3 || rows[2]["label"] != "x" {
		t.Fatalf("unfiltered PATCH = %v", rows)
	}
	rows = nil
	sendJSON(t, "DELETE", open.URL+"/api/e2e_bulk_probe", "", 200, &rows)
	if len(rows) != 3 {
		t.Fatalf("unfiltered DELETE = %v", rows)
	}
}
