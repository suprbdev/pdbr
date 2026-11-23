package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestPostResolution(t *testing.T) {
	sweep(t)
	srv := testServer(t, nil)
	merge := map[string]string{"Prefer": "resolution=merge-duplicates"}
	ignore := map[string]string{"Prefer": "resolution=ignore-duplicates"}
	email := uniqueEmail(t)

	res, body := send(t, "POST", srv.URL+"/api/users?on_conflict=email", `{"email":"`+email+`","full_name":"Ada","mood":"happy"}`, merge)
	var first row
	if res.StatusCode != 201 || json.Unmarshal(body, &first) != nil || res.Header.Get("Preference-Applied") != "resolution=merge-duplicates" {
		t.Fatalf("merge insert = %d %s %v", res.StatusCode, body, res.Header)
	}
	id := rowID(t, first)
	if res.Header.Get("Location") != fmt.Sprintf("/api/users/%d", id) {
		t.Errorf("Location = %q", res.Header.Get("Location"))
	}
	var merged row
	res, body = send(t, "POST", srv.URL+"/api/users?on_conflict=email", `{"email":"`+email+`","full_name":"Grace"}`, merge)
	if res.StatusCode != 201 || json.Unmarshal(body, &merged) != nil || rowID(t, merged) != id || merged["full_name"] != "Grace" || merged["mood"] != "happy" {
		t.Fatalf("merge update = %d %s", res.StatusCode, body)
	}

	email2 := uniqueEmail(t)
	var rows []row
	res, body = send(t, "POST", srv.URL+"/api/users?on_conflict=email&select=email,full_name,balance", `[{"email":"`+email+`","full_name":"Ignored"},{"email":"`+email2+`"}]`,
		map[string]string{"Prefer": "resolution=ignore-duplicates, missing=default"})
	if res.StatusCode != 201 || json.Unmarshal(body, &rows) != nil || len(rows) != 1 || rows[0]["email"] != email2 || rows[0]["balance"] != "0" {
		t.Fatalf("bulk ignore = %d %s", res.StatusCode, body)
	}
	if got := res.Header.Get("Preference-Applied"); got != "resolution=ignore-duplicates, missing=default" {
		t.Errorf("Preference-Applied = %q", got)
	}
	var kept row
	getJSON(t, fmt.Sprintf("%s/api/users/%d", srv.URL, id), &kept)
	if kept["full_name"] != "Grace" {
		t.Errorf("ignore-duplicates overwrote the existing row: %v", kept)
	}
	res, body = send(t, "POST", srv.URL+"/api/users?on_conflict=email", `{"email":"`+email+`"}`, ignore)
	if res.StatusCode != 204 || len(body) != 0 || res.Header.Get("Location") != "" {
		t.Fatalf("single ignored = %d %q %v", res.StatusCode, body, res.Header)
	}
	rows = nil
	res, body = send(t, "POST", srv.URL+"/api/users?on_conflict=email", `[{"email":"`+email+`","full_name":"M"},{"email":"`+email2+`","full_name":"M"}]`, merge)
	if res.StatusCode != 201 || json.Unmarshal(body, &rows) != nil || len(rows) != 2 || rows[1]["full_name"] != "M" {
		t.Fatalf("bulk merge = %d %s", res.StatusCode, body)
	}

	res, body = send(t, "POST", srv.URL+"/api/users?on_conflict=email", `{"email":"`+email+`"}`, nil)
	wantProblem(t, res, body, 400, "invalid_filter")
	res, body = send(t, "POST", srv.URL+"/api/users", `{"email":"`+email+`"}`, ignore)
	wantProblem(t, res, body, 409, "pg:23505")
}

func TestKeyedPut(t *testing.T) {
	sweep(t)
	srv := testServer(t, nil)
	email := uniqueEmail(t)
	path := srv.URL + "/api/users/by/email/" + url.PathEscape(email)
	var created row
	res, body := send(t, "PUT", path+"?select=id,email,full_name", `{"full_name":"Ada"}`, nil)
	if res.StatusCode != 201 || json.Unmarshal(body, &created) != nil || created["email"] != email {
		t.Fatalf("keyed PUT insert = %d %s", res.StatusCode, body)
	}
	if res.Header.Get("Location") != fmt.Sprintf("/api/users/%d", rowID(t, created)) {
		t.Errorf("Location = %q", res.Header.Get("Location"))
	}
	var updated row
	sendJSON(t, "PUT", path, `{"email":"`+email+`","mood":"ok"}`, 200, &updated)
	if rowID(t, updated) != rowID(t, created) || updated["full_name"] != "Ada" || updated["mood"] != "ok" {
		t.Fatalf("keyed PUT update = %v", updated)
	}
	res, body = send(t, "PUT", path, `{"email":"other@example.com"}`, nil)
	wantProblem(t, res, body, 400, "invalid_body")
	res, body = send(t, "PUT", path, `[{"mood":"ok"}]`, nil)
	wantProblem(t, res, body, 400, "invalid_body")
	res, body = send(t, "PUT", srv.URL+"/api/users/1", `{"full_name":"x"}`, nil)
	if wantProblem(t, res, body, 405, "method_not_allowed"); res.Header.Get("Allow") != "GET, HEAD, PATCH, DELETE" {
		t.Errorf("identity-key Allow = %q", res.Header.Get("Allow"))
	}
	res, body = send(t, "PUT", path, `{"mood":"ok"}`, map[string]string{"Prefer": "return=minimal"})
	if res.StatusCode != 204 || len(body) != 0 {
		t.Fatalf("keyed PUT minimal = %d %q", res.StatusCode, body)
	}

	dbURL := os.Getenv("PDBR_TEST_DATABASE_URL")
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `DROP TABLE IF EXISTS e2e_put_probe; CREATE TABLE e2e_put_probe (code text PRIMARY KEY, label text NOT NULL, note text)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), dbURL)
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		_, _ = c.Exec(context.Background(), `DROP TABLE IF EXISTS e2e_put_probe`)
	})
	probe := testServer(t, nil)
	base := probe.URL + "/api/e2e_put_probe/a%2Cb"
	var p row
	res = sendJSON(t, "PUT", base, `{"label":"x","note":"n"}`, 201, &p)
	if p["code"] != "a,b" || res.Header.Get("Location") != "/api/e2e_put_probe/a%2Cb" {
		t.Fatalf("PK PUT insert = %v %q", p, res.Header.Get("Location"))
	}
	sendJSON(t, "PUT", base, `{"code":"a,b","label":"y"}`, 200, &p)
	if p["label"] != "y" || p["note"] != "n" {
		t.Fatalf("PK PUT update keeps unprovided columns: %v", p)
	}
	res, body = send(t, "PUT", probe.URL+"/api/e2e_put_probe/new", `{"note":"x"}`, nil)
	wantProblem(t, res, body, 400, "invalid_body")
	res, body = send(t, "PUT", base, `{"code":"zz","label":"y"}`, nil)
	wantProblem(t, res, body, 400, "invalid_body")
}
