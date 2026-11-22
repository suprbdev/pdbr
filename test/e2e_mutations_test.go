package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbr/internal/config"
)

// send issues a request with a JSON body (Content-Type set when body != "").
func send(t *testing.T, method, url, body string, hdr map[string]string) (*http.Response, []byte) {
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
	return res, out
}

// sendJSON sends and decodes a JSON response, requiring status.
func sendJSON(t *testing.T, method, url, body string, status int, v any) *http.Response {
	t.Helper()
	res, out := send(t, method, url, body, nil)
	if res.StatusCode != status {
		t.Fatalf("%s %s = %d %s, want %d", method, url, res.StatusCode, out, status)
	}
	if v != nil {
		if err := json.Unmarshal(out, v); err != nil {
			t.Fatalf("%s %s: bad JSON %s: %v", method, url, out, err)
		}
	}
	return res
}

var uniqueSeq int

// uniqueEmail yields an address that no earlier run or test used, so the
// suite can be re-run against the same database.
func uniqueEmail(t *testing.T) string {
	uniqueSeq++
	return fmt.Sprintf("m3-%d-%d-%s@example.com", time.Now().UnixNano(), uniqueSeq, strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")))
}

// sweep removes whatever a mutation test left behind (it runs even after a
// Fatalf) so the read suite keeps seeing the pristine fixture.
func sweep(t *testing.T) {
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
			`DELETE FROM places WHERE name LIKE 'M3 %'`,
			`DELETE FROM posts WHERE title LIKE 'M3 %' OR author_id IN (SELECT id FROM users WHERE email LIKE 'm3-%')`,
			`DELETE FROM users WHERE email LIKE 'm3-%'`,
		} {
			if _, err := conn.Exec(ctx, q); err != nil {
				t.Logf("sweep: %v", err)
			}
		}
	})
}

func rowID(t *testing.T, r row) int {
	t.Helper()
	f, ok := r["id"].(float64)
	if !ok {
		t.Fatalf("row without numeric id: %v", r)
	}
	return int(f)
}

func TestCreatePatchDeleteRoundTrip(t *testing.T) {
	sweep(t)
	srv := testServer(t, nil)
	email := uniqueEmail(t)
	var created row
	res := sendJSON(t, "POST", srv.URL+"/api/users", `{"email": "`+email+`", "full_name": "Ada", "mood": "happy", "balance": "9007199254740993"}`, 201, &created)
	id := rowID(t, created)
	if loc := res.Header.Get("Location"); loc != fmt.Sprintf("/api/users/%d", id) {
		t.Errorf("Location = %q", loc)
	}
	if res.Header.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Errorf("content type %q", res.Header.Get("Content-Type"))
	}
	if created["email"] != email || created["full_name"] != "Ada" || created["mood"] != "happy" || created["balance"] != "9007199254740993" || created["settings"] != nil {
		t.Errorf("created row = %v", created)
	}
	if _, ok := created["created_at"]; !ok {
		t.Errorf("all columns expected in the response: %v", created)
	}
	var got row
	getJSON(t, srv.URL+res.Header.Get("Location"), &got)
	if got["email"] != email {
		t.Errorf("GET Location = %v", got)
	}

	var patched row
	sendJSON(t, "PATCH", fmt.Sprintf("%s/api/users/%d", srv.URL, id), `{"full_name": "Grace", "mood": null}`, 200, &patched)
	if patched["full_name"] != "Grace" || patched["mood"] != nil || patched["email"] != email {
		t.Errorf("patched = %v", patched)
	}
	// by-unique path, select= projection
	var byEmail row
	sendJSON(t, "PATCH", srv.URL+"/api/users/by/email/"+email+"?select=id,full_name", `{"full_name": "Grace H."}`, 200, &byEmail)
	if len(byEmail) != 2 || byEmail["full_name"] != "Grace H." {
		t.Errorf("patch by unique with select = %v", byEmail)
	}
	// updating the key column itself is allowed
	email2 := uniqueEmail(t)
	sendJSON(t, "PATCH", fmt.Sprintf("%s/api/users/%d", srv.URL, id), `{"email": "`+email2+`"}`, 200, &patched)
	if patched["email"] != email2 {
		t.Errorf("key update = %v", patched)
	}

	var deleted row
	sendJSON(t, "DELETE", fmt.Sprintf("%s/api/users/%d?select=id,email", srv.URL, id), "", 200, &deleted)
	if rowID(t, deleted) != id || deleted["email"] != email2 {
		t.Errorf("deleted = %v", deleted)
	}
	res, body := get(t, fmt.Sprintf("%s/api/users/%d", srv.URL, id))
	wantProblem(t, res, body, 404, "not_found")
	res, body = send(t, "DELETE", fmt.Sprintf("%s/api/users/%d", srv.URL, id), "", nil)
	wantProblem(t, res, body, 404, "not_found")
	res, body = send(t, "PATCH", fmt.Sprintf("%s/api/users/%d", srv.URL, id), `{"full_name": "x"}`, nil)
	wantProblem(t, res, body, 404, "not_found")
}

func TestCreateBulk(t *testing.T) {
	sweep(t)
	srv := testServer(t, nil)
	e1, e2 := uniqueEmail(t), uniqueEmail(t)
	var rows []row
	res := sendJSON(t, "POST", srv.URL+"/api/users?select=id,email,full_name,balance", `[{"email":"`+e1+`","full_name":"Ada"},{"email":"`+e2+`","balance":5}]`, 201, &rows)
	if res.Header.Get("Location") != "" {
		t.Errorf("bulk create must not carry Location: %q", res.Header.Get("Location"))
	}
	if len(rows) != 2 || rows[0]["email"] != e1 || rows[0]["full_name"] != "Ada" || rows[0]["balance"] != "0" ||
		rows[1]["email"] != e2 || rows[1]["full_name"] != nil || rows[1]["balance"] != "5" {
		t.Errorf("bulk rows = %v", rows)
	}
	for _, r := range rows {
		sendJSON(t, "DELETE", fmt.Sprintf("%s/api/users/%d", srv.URL, rowID(t, r)), "", 200, nil)
	}
	// one bad element fails the whole request before SQL
	res, body := send(t, "POST", srv.URL+"/api/users", `[{"email":"`+uniqueEmail(t)+`"},{"full_name":"no email"}]`, nil)
	p := wantProblem(t, res, body, 400, "invalid_body")
	if !strings.Contains(p.Detail, "element 1") || !strings.Contains(p.Detail, "email") {
		t.Errorf("detail = %q", p.Detail)
	}
	// atomic: a constraint failure on the second row rolls the first back
	dup := uniqueEmail(t)
	res, body = send(t, "POST", srv.URL+"/api/users", `[{"email":"`+dup+`"},{"email":"`+dup+`"}]`, nil)
	wantProblem(t, res, body, 409, "pg:23505")
	res, _ = get(t, srv.URL+"/api/users/by/email/"+dup)
	if res.StatusCode != 404 {
		t.Errorf("bulk insert leaked a row: %d", res.StatusCode)
	}
}

func TestBodyValidation(t *testing.T) {
	sweep(t)
	srv := testServer(t, func(c *config.Config) { c.Server.MaxBodyBytes = 200 })
	cases := []struct {
		name, method, url, body, ct string
		status                      int
		code                        string
	}{
		{"missing required", "POST", "/api/users", `{}`, "", 400, "invalid_body"},
		{"empty array", "POST", "/api/users", `[]`, "", 400, "invalid_body"},
		{"scalar body", "POST", "/api/users", `"x"`, "", 400, "invalid_body"},
		{"array element not object", "POST", "/api/users", `[1]`, "", 400, "invalid_body"},
		{"no body", "POST", "/api/users", ``, "", 400, "invalid_body"},
		{"invalid json", "POST", "/api/users", `{"email":`, "", 400, "invalid_body"},
		{"trailing data", "POST", "/api/users", `{"email":"x@y.z"} {}`, "", 400, "invalid_body"},
		{"unknown key", "POST", "/api/users", `{"email":"x@y.z","nope":1}`, "", 400, "invalid_body"},
		{"relation key without plugin", "POST", "/api/users", `{"email":"x@y.z","posts":{"create":[]}}`, "", 400, "invalid_body"},
		{"generated column", "POST", "/api/users", `{"email":"x@y.z","id":5}`, "", 400, "column_readonly"},
		{"uncoercible", "POST", "/api/users", `{"email":{"a":1}}`, "", 400, "invalid_body"},
		{"bad int", "POST", "/api/posts", `{"author_id":"abc","title":"t"}`, "", 400, "invalid_body"},
		{"bad enum", "POST", "/api/users", `{"email":"x@y.z","mood":"angry"}`, "", 400, "invalid_body"},
		{"array for scalar", "POST", "/api/users", `{"email":["x@y.z"]}`, "", 400, "invalid_body"},
		{"scalar for array", "POST", "/api/users", `{"email":"x@y.z","tags":"admin"}`, "", 400, "invalid_body"},
		{"composite not object", "POST", "/api/users", `{"email":"x@y.z","address":"here"}`, "", 400, "invalid_body"},
		{"composite unknown attr", "POST", "/api/users", `{"email":"x@y.z","address":{"zip":"1"}}`, "", 400, "invalid_body"},
		{"patch empty", "PATCH", "/api/users/1", `{}`, "", 400, "invalid_body"},
		{"patch array", "PATCH", "/api/users/1", `[{"full_name":"x"}]`, "", 400, "invalid_body"},
		{"patch unknown", "PATCH", "/api/users/1", `{"nope":1}`, "", 400, "invalid_body"},
		{"patch readonly", "PATCH", "/api/users/1", `{"id":9}`, "", 400, "column_readonly"},
		{"put no conflict target in body", "PUT", "/api/users?on_conflict=email", `{"full_name":"x"}`, "", 400, "invalid_body"},
		{"put pk is generated", "PUT", "/api/users", `{"email":"x@y.z"}`, "", 400, "invalid_body"},
		{"put bad target", "PUT", "/api/users?on_conflict=mood", `{"email":"x@y.z","mood":"ok"}`, "", 400, "invalid_body"},
		{"put unknown target", "PUT", "/api/users?on_conflict=zzz", `{"email":"x@y.z"}`, "", 400, "unknown_column"},
		{"filters on mutation", "POST", "/api/users?mood=eq.happy", `{"email":"x@y.z"}`, "", 400, "invalid_filter"},
		{"limit on mutation", "POST", "/api/users?limit=1", `{"email":"x@y.z"}`, "", 400, "invalid_filter"},
		{"wrong content type", "POST", "/api/users", `{"email":"x@y.z"}`, "text/plain", 415, "unsupported_media_type"},
		{"too large", "POST", "/api/users", `{"email":"x@y.z","full_name":"` + strings.Repeat("a", 300) + `"}`, "", 413, "body_too_large"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hdr := map[string]string{}
			if tc.ct != "" {
				hdr["Content-Type"] = tc.ct
			}
			res, body := send(t, tc.method, srv.URL+tc.url, tc.body, hdr)
			wantProblem(t, res, body, tc.status, tc.code)
		})
	}
	// database-side validation: NOT NULL via explicit null, FK, unique, delete with dependents
	res, body := send(t, "POST", srv.URL+"/api/posts", `{"author_id": 1, "title": null}`, nil)
	wantProblem(t, res, body, 400, "pg:23502")
	res, body = send(t, "POST", srv.URL+"/api/posts", `{"author_id": 999999, "title": "orphan"}`, nil)
	wantProblem(t, res, body, 409, "pg:23503")
	res, body = send(t, "POST", srv.URL+"/api/users", `{"email": "ada@example.com"}`, nil)
	p := wantProblem(t, res, body, 409, "pg:23505")
	if p.PG["constraint"] != "users_email_key" {
		t.Errorf("dev pg object = %v", p.PG)
	}
	res, body = send(t, "DELETE", srv.URL+"/api/users/1", "", nil)
	wantProblem(t, res, body, 409, "pg:23503")
}

func TestIfMatch(t *testing.T) {
	sweep(t)
	srv := testServer(t, nil)
	var u row
	sendJSON(t, "POST", srv.URL+"/api/users", `{"email": "`+uniqueEmail(t)+`"}`, 201, &u)
	id := rowID(t, u)
	path := fmt.Sprintf("%s/api/users/%d", srv.URL, id)
	res, _ := get(t, path)
	etag := res.Header.Get("ETag")
	if !strings.HasPrefix(etag, `W/"`) {
		t.Fatalf("no ETag: %q", etag)
	}
	res, body := send(t, "PATCH", path, `{"full_name":"x"}`, map[string]string{"If-Match": `W/"0123456789abcdef0123456789abcdef"`})
	wantProblem(t, res, body, 412, "etag_mismatch")
	var got row
	getJSON(t, path, &got)
	if got["full_name"] != nil {
		t.Errorf("412 must not write: %v", got)
	}
	// multiple tags, one matching; strong form accepted too
	strong := strings.TrimPrefix(etag, "W/")
	res, body = send(t, "PATCH", path, `{"full_name":"x"}`, map[string]string{"If-Match": `W/"nope", ` + strong})
	if res.StatusCode != 200 {
		t.Fatalf("matching If-Match = %d %s", res.StatusCode, body)
	}
	// the row changed, so the old tag is now stale
	res, body = send(t, "PATCH", path, `{"full_name":"y"}`, map[string]string{"If-Match": etag})
	wantProblem(t, res, body, 412, "etag_mismatch")
	// * = must exist; missing rows are 404 (before 412)
	res, body = send(t, "PATCH", path, `{"full_name":"z"}`, map[string]string{"If-Match": "*"})
	if res.StatusCode != 200 {
		t.Fatalf("If-Match * = %d %s", res.StatusCode, body)
	}
	res, body = send(t, "PATCH", srv.URL+"/api/users/999999999", `{"full_name":"z"}`, map[string]string{"If-Match": etag})
	wantProblem(t, res, body, 404, "not_found")
	res, body = send(t, "DELETE", path, "", map[string]string{"If-Match": `W/"stale"`})
	wantProblem(t, res, body, 412, "etag_mismatch")
	res, body = send(t, "DELETE", path, "", map[string]string{"If-Match": "*", "Prefer": "return=minimal"})
	if res.StatusCode != 204 || len(body) != 0 {
		t.Fatalf("DELETE If-Match * minimal = %d %s", res.StatusCode, body)
	}
	// server.etags: false ignores If-Match entirely
	srv2 := testServer(t, func(c *config.Config) { c.Server.Etags = false })
	sendJSON(t, "POST", srv2.URL+"/api/users", `{"email": "`+uniqueEmail(t)+`"}`, 201, &u)
	path = fmt.Sprintf("%s/api/users/%d", srv2.URL, rowID(t, u))
	res, body = send(t, "PATCH", path, `{"full_name":"x"}`, map[string]string{"If-Match": `W/"stale"`})
	if res.StatusCode != 200 {
		t.Fatalf("etags off: %d %s", res.StatusCode, body)
	}
	sendJSON(t, "DELETE", path, "", 200, nil)
}

func TestPreferMinimal(t *testing.T) {
	sweep(t)
	srv := testServer(t, nil)
	minimal := map[string]string{"Prefer": "return=minimal"}
	email := uniqueEmail(t)
	res, body := send(t, "POST", srv.URL+"/api/users", `{"email": "`+email+`"}`, minimal)
	if res.StatusCode != 201 || len(body) != 0 || !strings.HasPrefix(res.Header.Get("Location"), "/api/users/") {
		t.Fatalf("POST minimal = %d %q loc=%q", res.StatusCode, body, res.Header.Get("Location"))
	}
	path := srv.URL + res.Header.Get("Location")
	res, body = send(t, "PATCH", path, `{"full_name":"x"}`, minimal)
	if res.StatusCode != 204 || len(body) != 0 || res.Header.Get("Content-Type") != "" {
		t.Fatalf("PATCH minimal = %d %q %q", res.StatusCode, body, res.Header.Get("Content-Type"))
	}
	var got row
	getJSON(t, path, &got)
	if got["full_name"] != "x" {
		t.Errorf("minimal patch not applied: %v", got)
	}
	// upsert: update -> 204, insert -> 201 + Location
	res, body = send(t, "PUT", srv.URL+"/api/users?on_conflict=email", `{"email": "`+email+`", "full_name": "y"}`, minimal)
	if res.StatusCode != 204 || len(body) != 0 {
		t.Fatalf("PUT minimal update = %d %q", res.StatusCode, body)
	}
	email2 := uniqueEmail(t)
	res, body = send(t, "PUT", srv.URL+"/api/users?on_conflict=email", `{"email": "`+email2+`"}`, minimal)
	if res.StatusCode != 201 || len(body) != 0 || res.Header.Get("Location") == "" {
		t.Fatalf("PUT minimal insert = %d %q", res.StatusCode, body)
	}
	res, body = send(t, "POST", srv.URL+"/api/users", `[{"email": "`+uniqueEmail(t)+`"}]`, minimal)
	if res.StatusCode != 201 || len(body) != 0 || res.Header.Get("Location") != "" {
		t.Fatalf("bulk POST minimal = %d %q", res.StatusCode, body)
	}
	res, body = send(t, "DELETE", path, "", minimal)
	if res.StatusCode != 204 || len(body) != 0 {
		t.Fatalf("DELETE minimal = %d %q", res.StatusCode, body)
	}
	res, body = send(t, "DELETE", path, "", minimal)
	wantProblem(t, res, body, 404, "not_found")
	sendJSON(t, "DELETE", srv.URL+"/api/users/by/email/"+email2, "", 200, nil)
}

func TestUpsert(t *testing.T) {
	sweep(t)
	srv := testServer(t, nil)
	email := uniqueEmail(t)
	var r row
	res := sendJSON(t, "PUT", srv.URL+"/api/users?on_conflict=email", `{"email": "`+email+`", "full_name": "Ada", "mood": "happy"}`, 201, &r)
	id := rowID(t, r)
	if res.Header.Get("Location") != fmt.Sprintf("/api/users/%d", id) || r["full_name"] != "Ada" {
		t.Errorf("insert branch: %v %q", r, res.Header.Get("Location"))
	}
	res = sendJSON(t, "PUT", srv.URL+"/api/users?on_conflict=email", `{"email": "`+email+`", "full_name": "Grace"}`, 200, &r)
	if rowID(t, r) != id || r["full_name"] != "Grace" || r["mood"] != "happy" || res.Header.Get("Location") != "" {
		t.Errorf("update branch keeps unprovided columns: %v", r)
	}
	// only-key body returns the existing row unchanged
	var narrow row
	sendJSON(t, "PUT", srv.URL+"/api/users?on_conflict=email&select=id,full_name", `{"email": "`+email+`"}`, 200, &narrow)
	if rowID(t, narrow) != id || narrow["full_name"] != "Grace" || len(narrow) != 2 {
		t.Errorf("only-key upsert: %v", narrow)
	}
	// bulk: always 200 + array, mixed insert/update
	email2 := uniqueEmail(t)
	var rows []row
	res = sendJSON(t, "PUT", srv.URL+"/api/users?on_conflict=email", `[{"email":"`+email+`","full_name":"Ada L."},{"email":"`+email2+`","mood":"ok"}]`, 200, &rows)
	if len(rows) != 2 || rows[0]["full_name"] != "Ada L." || rows[1]["mood"] != "ok" || res.Header.Get("Location") != "" {
		t.Errorf("bulk upsert: %v", rows)
	}
	for _, r := range rows {
		sendJSON(t, "DELETE", fmt.Sprintf("%s/api/users/%d", srv.URL, rowID(t, r)), "", 200, nil)
	}
	// posts: PK is generated, so PUT needs another unique constraint; posts has none -> 400
	res2, body := send(t, "PUT", srv.URL+"/api/posts", `{"author_id":1,"title":"x"}`, nil)
	wantProblem(t, res2, body, 400, "invalid_body")
}

func TestMutationSelectAndEmbeds(t *testing.T) {
	sweep(t)
	srv := testServer(t, nil)
	var p row
	res := sendJSON(t, "POST", srv.URL+"/api/posts?select=id,title,users(id,email),comments(id)", `{"author_id": 1, "title": "M3 embed"}`, 201, &p)
	if res.Header.Get("Location") != fmt.Sprintf("/api/posts/%d", rowID(t, p)) {
		t.Errorf("Location %q", res.Header.Get("Location"))
	}
	author, _ := p["users"].(map[string]any)
	if len(p) != 4 || author["email"] != "ada@example.com" || p["title"] != "M3 embed" {
		t.Errorf("embedded create = %v", p)
	}
	if list, ok := p["comments"].([]any); !ok || len(list) != 0 {
		t.Errorf("backward embed on create = %v", p["comments"])
	}
	sendJSON(t, "DELETE", fmt.Sprintf("%s/api/posts/%d", srv.URL, rowID(t, p)), "", 200, nil)
	// alias + computed column on the mutation response (computed columns
	// force every column into RETURNING)
	var u row
	sendJSON(t, "POST", srv.URL+"/api/users?select=mail:email,post_count", `{"email": "`+uniqueEmail(t)+`"}`, 201, &u)
	if len(u) != 2 || u["post_count"] != "0" || u["mail"] == nil {
		t.Errorf("create with alias/computed = %v", u)
	}
	var d row
	sendJSON(t, "DELETE", fmt.Sprintf("%s/api/users/by/email/%s?select=mail:email,post_count", srv.URL, u["mail"]), "", 200, &d)
	if len(d) != 2 || d["mail"] != u["mail"] {
		t.Errorf("delete with alias/computed = %v", d)
	}
}

func TestTypedInputs(t *testing.T) {
	sweep(t)
	srv := testServer(t, nil)
	email := uniqueEmail(t)
	body := `{"email": "` + email + `", "mood": "ok", "moods": ["sad", "happy"], "tags": ["a", "b"],
		"settings": {"theme": "dark", "n": [1, 2]}, "balance": 12345678901234,
		"address": {"street": "12 St James Sq", "city": "London", "mood": "happy"},
		"prev_addresses": [{"street": "1 Ockham Park", "city": "Surrey"}, null]}`
	var u row
	sendJSON(t, "POST", srv.URL+"/api/users", body, 201, &u)
	defer sendJSON(t, "DELETE", fmt.Sprintf("%s/api/users/%d", srv.URL, rowID(t, u)), "", 200, nil)
	settings, _ := u["settings"].(map[string]any)
	addr, _ := u["address"].(map[string]any)
	prev, _ := u["prev_addresses"].([]any)
	if u["mood"] != "ok" || fmt.Sprint(u["moods"]) != "[sad happy]" || fmt.Sprint(u["tags"]) != "[a b]" ||
		settings["theme"] != "dark" || u["balance"] != "12345678901234" ||
		addr["street"] != "12 St James Sq" || addr["mood"] != "happy" || len(prev) != 2 || prev[1] != nil {
		t.Errorf("typed inputs round trip = %v", u)
	}
	if first, _ := prev[0].(map[string]any); first["city"] != "Surrey" || first["mood"] != nil {
		t.Errorf("composite array element = %v", prev[0])
	}
	// nulls for typed columns
	var patched row
	sendJSON(t, "PATCH", fmt.Sprintf("%s/api/users/%d", srv.URL, rowID(t, u)), `{"address": null, "prev_addresses": null, "settings": null, "moods": null}`, 200, &patched)
	if patched["address"] != nil || patched["prev_addresses"] != nil || patched["settings"] != nil || patched["moods"] != nil {
		t.Errorf("nulls = %v", patched)
	}
	// empty composite array stays an empty array
	sendJSON(t, "PATCH", fmt.Sprintf("%s/api/users/%d", srv.URL, rowID(t, u)), `{"prev_addresses": []}`, 200, &patched)
	if list, ok := patched["prev_addresses"].([]any); !ok || len(list) != 0 {
		t.Errorf("empty composite array = %v", patched["prev_addresses"])
	}
	// spatial: GeoJSON and EWKT inputs
	var pl row
	sendJSON(t, "POST", srv.URL+"/api/places", `{"name": "M3 place", "owner_id": 1,
		"location": {"type": "Point", "coordinates": [-0.1, 51.5]},
		"area": "SRID=4326;POLYGON((-0.130 51.507, -0.126 51.507, -0.126 51.509, -0.130 51.509, -0.130 51.507))"}`, 201, &pl)
	defer sendJSON(t, "DELETE", fmt.Sprintf("%s/api/places/%d", srv.URL, rowID(t, pl)), "", 200, nil)
	loc, _ := pl["location"].(map[string]any)
	area, _ := pl["area"].(map[string]any)
	if loc["type"] != "Point" || area["type"] != "Polygon" {
		t.Errorf("spatial round trip = %v", pl)
	}
	res, b := send(t, "POST", srv.URL+"/api/places", `{"name": "bad", "location": "not wkt"}`, nil)
	wantProblem(t, res, b, 400, "invalid_body")
}

func TestMutationMethodAvailability(t *testing.T) {
	srv := testServer(t, nil)
	res, body := send(t, "OPTIONS", srv.URL+"/api/users", "", nil)
	if wantProblem(t, res, body, 405, "method_not_allowed"); res.Header.Get("Allow") != "GET, HEAD, POST, PUT, PATCH, DELETE" {
		t.Errorf("Allow = %q", res.Header.Get("Allow"))
	}
	res, body = send(t, "DELETE", srv.URL+"/api/metrics?name=eq.x", "", nil)
	if wantProblem(t, res, body, 405, "method_not_allowed"); res.Header.Get("Allow") != "GET, HEAD" {
		t.Errorf("view Allow = %q", res.Header.Get("Allow"))
	}
	res, body = send(t, "POST", srv.URL+"/api/metrics", `{"name":"x"}`, nil)
	if wantProblem(t, res, body, 405, "method_not_allowed"); res.Header.Get("Allow") != "GET, HEAD" {
		t.Errorf("view Allow = %q", res.Header.Get("Allow"))
	}
	res, body = send(t, "POST", srv.URL+"/api/users/1", `{}`, nil)
	if wantProblem(t, res, body, 405, "method_not_allowed"); res.Header.Get("Allow") != "GET, HEAD, PATCH, DELETE" {
		t.Errorf("row Allow = %q", res.Header.Get("Allow"))
	}
	ro := testServer(t, func(c *config.Config) { c.Server.ReadOnly = true })
	res, body = send(t, "PATCH", ro.URL+"/api/users/1", `{"full_name":"x"}`, nil)
	if wantProblem(t, res, body, 405, "read_only"); res.Header.Get("Allow") != "GET, HEAD" {
		t.Errorf("read-only Allow = %q", res.Header.Get("Allow"))
	}
	res, body = send(t, "PUT", ro.URL+"/api/users?on_conflict=email", `{"email":"x@y.z"}`, nil)
	wantProblem(t, res, body, 405, "read_only")
	res, body = send(t, "DELETE", ro.URL+"/api/users?id=eq.1", "", nil)
	if wantProblem(t, res, body, 405, "read_only"); res.Header.Get("Allow") != "GET, HEAD" {
		t.Errorf("read-only bulk Allow = %q", res.Header.Get("Allow"))
	}
}

func TestMutationPrivilegesAndRLS(t *testing.T) {
	sweep(t)
	url := os.Getenv("PDBR_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	// A role holding column-level SELECT on users only (idempotent).
	if _, err := conn.Exec(ctx, `DO $$ BEGIN
		IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'e2e_partial') THEN CREATE ROLE e2e_partial NOLOGIN; END IF; END $$;
		GRANT USAGE ON SCHEMA public TO e2e_partial;
		GRANT SELECT (id, email), INSERT ON users TO e2e_partial;`); err != nil {
		t.Fatal(err)
	}
	srv := testServer(t, func(c *config.Config) {
		c.RLS.Enabled = true
		c.RLS.Auth.Mode = "headers"
	})
	as := func(role, user string) map[string]string {
		h := map[string]string{"X-Pdbr-Claim-Role": role}
		if user != "" {
			h["X-Pdbr-Claim-User-Id"] = user
		}
		return h
	}
	// anonymous has no INSERT on users
	res, body := send(t, "POST", srv.URL+"/api/users", `{"email":"`+uniqueEmail(t)+`"}`, nil)
	wantProblem(t, res, body, 403, "pg:42501")
	// app_user may write, and RLS scopes writes to own posts
	var post row
	res, body = send(t, "POST", srv.URL+"/api/posts?select=id,title", `{"author_id": 1, "title": "M3 rls"}`, as("app_user", "1"))
	if res.StatusCode != 201 {
		t.Fatalf("app_user create = %d %s", res.StatusCode, body)
	}
	if err := json.Unmarshal(body, &post); err != nil {
		t.Fatal(err)
	}
	pid := rowID(t, post)
	res, body = send(t, "POST", srv.URL+"/api/posts", `{"author_id": 1, "title": "M3 not mine"}`, as("app_user", "2"))
	wantProblem(t, res, body, 403, "pg:42501")
	res, body = send(t, "PATCH", fmt.Sprintf("%s/api/posts/%d", srv.URL, pid), `{"title":"hidden"}`, as("app_user", "2"))
	wantProblem(t, res, body, 404, "not_found")
	res, body = send(t, "PATCH", fmt.Sprintf("%s/api/posts/%d", srv.URL, pid), `{"title":"mine"}`, as("app_user", "1"))
	if res.StatusCode != 200 {
		t.Fatalf("owner patch = %d %s", res.StatusCode, body)
	}
	res, body = send(t, "DELETE", fmt.Sprintf("%s/api/posts/%d", srv.URL, pid), "", as("app_user", "1"))
	if res.StatusCode != 200 {
		t.Fatalf("owner delete = %d %s", res.StatusCode, body)
	}
	// RETURNING follows select=: within the column grant it works, beyond it 42501
	email := uniqueEmail(t)
	res, body = send(t, "POST", srv.URL+"/api/users?select=id,email", `{"email":"`+email+`"}`, as("e2e_partial", ""))
	if res.StatusCode != 201 {
		t.Fatalf("partial-grant create = %d %s", res.StatusCode, body)
	}
	res, body = send(t, "POST", srv.URL+"/api/users", `{"email":"`+uniqueEmail(t)+`"}`, as("e2e_partial", ""))
	wantProblem(t, res, body, 403, "pg:42501")
	res, body = send(t, "DELETE", srv.URL+"/api/users/by/email/"+email, "", as("app_user", ""))
	if res.StatusCode != 200 {
		t.Fatalf("cleanup = %d %s", res.StatusCode, body)
	}
}

func TestMutationTransactionsOff(t *testing.T) {
	sweep(t)
	srv := testServer(t, func(c *config.Config) { c.TX.Mutations = false })
	var u row
	sendJSON(t, "POST", srv.URL+"/api/users", `{"email":"`+uniqueEmail(t)+`"}`, 201, &u)
	sendJSON(t, "DELETE", fmt.Sprintf("%s/api/users/%d", srv.URL, rowID(t, u)), "", 200, nil)
}
