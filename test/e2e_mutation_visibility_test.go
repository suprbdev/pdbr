package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"slices"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbr/internal/config"
)

// embedded walks a decoded response through object keys and returns the
// value at the end of the path.
func embedded(t *testing.T, v any, path ...string) any {
	t.Helper()
	for _, k := range path {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("%v: no object at %q", v, k)
		}
		v = m[k]
	}
	return v
}

// embeddedRows returns the array at path as objects.
func embeddedRows(t *testing.T, v any, path ...string) []map[string]any {
	t.Helper()
	list, ok := embedded(t, v, path...).([]any)
	if !ok {
		t.Fatalf("%v: no array at %v", v, path)
	}
	out := make([]map[string]any, len(list))
	for i, item := range list {
		out[i], ok = item.(map[string]any)
		if !ok {
			t.Fatalf("%v: element %d is not an object", list, i)
		}
	}
	return out
}

// sortedIDs returns the sorted integer ids of rows.
func sortedIDs(t *testing.T, rows []map[string]any) []int {
	t.Helper()
	out := make([]int, len(rows))
	for i, r := range rows {
		out[i] = rowID(t, r)
	}
	sort.Ints(out)
	return out
}

func TestMutationVisibility(t *testing.T) {
	sweep(t)
	srv := testServer(t, nil)
	email := uniqueEmail(t)
	var u row
	sendJSON(t, "POST", srv.URL+"/api/users", `{"email":"`+email+`"}`, 201, &u)
	uid := rowID(t, u)

	var p row
	sendJSON(t, "POST", srv.URL+"/api/posts?select=id,users(email,posts(id,title))", fmt.Sprintf(`{"author_id":%d,"title":"M3 vis one"}`, uid), 201, &p)
	p1 := rowID(t, p)
	if got := sortedIDs(t, embeddedRows(t, p, "users", "posts")); !slices.Equal(got, []int{p1}) {
		t.Errorf("create: embedded posts = %v, want [%d]", got, p1)
	}

	var bulk []row
	sendJSON(t, "POST", srv.URL+"/api/posts?select=id,users(posts(id))", fmt.Sprintf(`[{"author_id":%d,"title":"M3 vis two"},{"author_id":%d,"title":"M3 vis three"}]`, uid, uid), 201, &bulk)
	if len(bulk) != 2 {
		t.Fatalf("bulk create = %v", bulk)
	}
	p2, p3 := rowID(t, bulk[0]), rowID(t, bulk[1])
	all := []int{p1, p2, p3}
	sort.Ints(all)
	for _, r := range bulk {
		if got := sortedIDs(t, embeddedRows(t, r, "users", "posts")); !slices.Equal(got, all) {
			t.Errorf("bulk create: embedded posts = %v, want %v", got, all)
		}
	}

	var f row
	sendJSON(t, "POST", srv.URL+"/api/posts?select=id,users(posts(id))&users.posts.title=like.M3%20vis%20f*&users.posts.order=id.desc&users.posts.limit=1",
		fmt.Sprintf(`{"author_id":%d,"title":"M3 vis four"}`, uid), 201, &f)
	p4 := rowID(t, f)
	if got := sortedIDs(t, embeddedRows(t, f, "users", "posts")); !slices.Equal(got, []int{p4}) {
		t.Errorf("create with embed filter/order/limit: embedded posts = %v, want [%d]", got, p4)
	}
	sendJSON(t, "POST", srv.URL+"/api/posts?select=id,users(id)&users.posts.title=eq.M3%20vis%20five", fmt.Sprintf(`{"author_id":%d,"title":"M3 vis five"}`, uid), 201, &f)
	p5 := rowID(t, f)
	if embedded(t, f, "users") == nil {
		t.Errorf("create: relation filter on the embed misses the new row: %v", f)
	}
	all = append(all, p4, p5)
	sort.Ints(all)

	var patched row
	sendJSON(t, "PATCH", fmt.Sprintf("%s/api/posts/%d?select=id,title,users(posts(id,title))", srv.URL, p1), `{"title":"M3 vis patched"}`, 200, &patched)
	posts := embeddedRows(t, patched, "users", "posts")
	if got := sortedIDs(t, posts); !slices.Equal(got, all) {
		t.Errorf("patch: embedded posts = %v, want %v", got, all)
	}
	for _, q := range posts {
		if rowID(t, q) == p1 && q["title"] != "M3 vis patched" {
			t.Errorf("patch: embedded post %d = %v, want the new title", p1, q)
		}
	}

	email2 := uniqueEmail(t)
	var renamed row
	sendJSON(t, "PATCH", srv.URL+"/api/users/by/email/"+url.PathEscape(email)+"?select=id,email,posts(users(email))", `{"email":"`+email2+`"}`, 200, &renamed)
	for _, q := range embeddedRows(t, renamed, "posts") {
		if got := embedded(t, q, "users", "email"); got != email2 {
			t.Errorf("patch changing the key: embedded author email = %v, want %s", got, email2)
		}
	}
	email = email2

	userPath := fmt.Sprintf("%s/api/users/%d", srv.URL, uid)
	res, _ := get(t, userPath)
	var ifm row
	res, body := send(t, "PATCH", userPath+"?select=id,posts(users(full_name))", `{"full_name":"M3 if-match"}`, map[string]string{"If-Match": res.Header.Get("ETag")})
	if res.StatusCode != 200 {
		t.Fatalf("If-Match patch = %d %s", res.StatusCode, body)
	}
	decode(t, body, &ifm)
	for _, q := range embeddedRows(t, ifm, "posts") {
		if got := embedded(t, q, "users", "full_name"); got != "M3 if-match" {
			t.Errorf("If-Match patch: embedded full_name = %v", got)
		}
	}

	var put row
	sendJSON(t, "PUT", srv.URL+"/api/users/by/email/"+url.PathEscape(email)+"?select=id,posts(users(full_name))", `{"full_name":"M3 put"}`, 200, &put)
	for _, q := range embeddedRows(t, put, "posts") {
		if got := embedded(t, q, "users", "full_name"); got != "M3 put" {
			t.Errorf("keyed PUT: embedded full_name = %v", got)
		}
	}
	var puts []row
	sendJSON(t, "PUT", srv.URL+"/api/users?on_conflict=email&select=email,posts(users(full_name))", `[{"email":"`+email+`","full_name":"M3 bulk put"}]`, 200, &puts)
	if len(puts) != 1 {
		t.Fatalf("bulk PUT = %v", puts)
	}
	for _, q := range embeddedRows(t, puts[0], "posts") {
		if got := embedded(t, q, "users", "full_name"); got != "M3 bulk put" {
			t.Errorf("bulk PUT: embedded full_name = %v", got)
		}
	}
	var merged row
	res, body = send(t, "POST", srv.URL+"/api/users?on_conflict=email&select=id,posts(users(full_name))", `{"email":"`+email+`","full_name":"M3 merged"}`,
		map[string]string{"Prefer": "return=representation,resolution=merge-duplicates"})
	if res.StatusCode != 200 && res.StatusCode != 201 {
		t.Fatalf("merge-duplicates = %d %s", res.StatusCode, body)
	}
	decode(t, body, &merged)
	for _, q := range embeddedRows(t, merged, "posts") {
		if got := embedded(t, q, "users", "full_name"); got != "M3 merged" {
			t.Errorf("merge-duplicates: embedded full_name = %v", got)
		}
	}

	var rows []row
	sendJSON(t, "PATCH", fmt.Sprintf("%s/api/posts?author_id=eq.%d&select=id,users(posts(id,title))", srv.URL, uid), `{"title":"M3 vis bulk"}`, 200, &rows)
	if len(rows) != len(all) {
		t.Fatalf("bulk patch = %v", rows)
	}
	for _, r := range rows {
		posts := embeddedRows(t, r, "users", "posts")
		if got := sortedIDs(t, posts); !slices.Equal(got, all) {
			t.Errorf("bulk patch: embedded posts = %v, want %v", got, all)
		}
		for _, q := range posts {
			if q["title"] != "M3 vis bulk" {
				t.Errorf("bulk patch: embedded post = %v", q)
			}
		}
	}

	var del row
	sendJSON(t, "DELETE", fmt.Sprintf("%s/api/posts/%d?select=id,users(posts(id))", srv.URL, p1), "", 200, &del)
	all = slices.DeleteFunc(all, func(id int) bool { return id == p1 })
	if got := sortedIDs(t, embeddedRows(t, del, "users", "posts")); !slices.Equal(got, all) {
		t.Errorf("delete: embedded posts = %v, want %v", got, all)
	}
	sendJSON(t, "DELETE", fmt.Sprintf("%s/api/posts?id=in.(%d,%d)&select=id,users(posts(id))", srv.URL, p2, p3), "", 200, &rows)
	all = slices.DeleteFunc(all, func(id int) bool { return id == p2 || id == p3 })
	if len(rows) != 2 {
		t.Fatalf("bulk delete = %v", rows)
	}
	for _, r := range rows {
		if got := sortedIDs(t, embeddedRows(t, r, "users", "posts")); !slices.Equal(got, all) {
			t.Errorf("bulk delete: embedded posts = %v, want %v", got, all)
		}
	}

	var nested row
	nestedEmail := uniqueEmail(t)
	sendJSON(t, "POST", srv.URL+"/api/users?select=id,posts(id,users(email,posts(id)))&posts.limit=5&posts.users.posts.limit=5",
		`{"email":"`+nestedEmail+`","posts":{"create":[{"title":"M3 vis nested a"},{"title":"M3 vis nested b"}]}}`, 201, &nested)
	children := embeddedRows(t, nested, "posts")
	if len(children) != 2 {
		t.Fatalf("nested create = %v", nested)
	}
	want := sortedIDs(t, children)
	for _, c := range children {
		if got := embedded(t, c, "users", "email"); got != nestedEmail {
			t.Errorf("nested create: the child's author = %v, want the new user", got)
		}
		if got := sortedIDs(t, embeddedRows(t, c, "users", "posts")); !slices.Equal(got, want) {
			t.Errorf("nested create: the new user's posts = %v, want %v", got, want)
		}
	}
}

// TestMutationVisibilityFallback covers a bulk update patching every row
// key, where the visibility CTE falls back to the request's filter.
func TestMutationVisibilityFallback(t *testing.T) {
	dbURL := os.Getenv("PDBR_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set")
	}
	execSQL(t, dbURL, `DROP TABLE IF EXISTS mvis_codes, mvis_groups;
		CREATE TABLE mvis_groups (id integer PRIMARY KEY);
		CREATE TABLE mvis_codes (code text PRIMARY KEY, label text NOT NULL, group_id integer NOT NULL REFERENCES mvis_groups (id));
		CREATE INDEX mvis_codes_group_id_idx ON mvis_codes (group_id);
		INSERT INTO mvis_groups VALUES (1), (2);
		INSERT INTO mvis_codes VALUES ('a', 'A', 1), ('b', 'B', 1), ('c', 'C', 2);`)
	t.Cleanup(func() { execSQL(t, dbURL, `DROP TABLE IF EXISTS mvis_codes, mvis_groups`) })
	srv := testServer(t, nil)
	var rows []row
	sendJSON(t, "PATCH", srv.URL+"/api/mvis_codes?code=eq.a&select=code,mvis_groups(mvis_codes(code,label))", `{"code":"z","label":"Z"}`, 200, &rows)
	if len(rows) != 1 {
		t.Fatalf("bulk patch = %v", rows)
	}
	codes := embeddedRows(t, rows[0], "mvis_groups", "mvis_codes")
	var got []string
	for _, c := range codes {
		got = append(got, fmt.Sprint(c["code"], "=", c["label"]))
	}
	sort.Strings(got)
	if !slices.Equal(got, []string{"b=B", "z=Z"}) {
		t.Errorf("bulk patch of the key: embedded codes = %v", got)
	}
}

// TestMutationVisibilityRLS runs the visibility CTE under a role whose
// SELECT policy is wider than its UPDATE/DELETE policy and whose SELECT
// grant covers only some columns.
func TestMutationVisibilityRLS(t *testing.T) {
	dbURL := os.Getenv("PDBR_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set")
	}
	execSQL(t, dbURL, `DROP TABLE IF EXISTS mvis_notes, mvis_boards;
		DO $$ BEGIN
			IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'e2e_mvis') THEN CREATE ROLE e2e_mvis NOLOGIN; END IF; END $$;
		GRANT USAGE ON SCHEMA public TO e2e_mvis;
		CREATE TABLE mvis_boards (id integer PRIMARY KEY, name text NOT NULL);
		CREATE TABLE mvis_notes (
			id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			board_id integer NOT NULL REFERENCES mvis_boards (id),
			owner integer NOT NULL,
			body text NOT NULL,
			secret text
		);
		CREATE INDEX mvis_notes_board_id_idx ON mvis_notes (board_id);
		INSERT INTO mvis_boards VALUES (1, 'one');
		INSERT INTO mvis_notes (board_id, owner, body, secret) VALUES (1, 1, 'mine', 's1'), (1, 2, 'theirs', 's2');
		GRANT SELECT ON mvis_boards TO e2e_mvis;
		GRANT SELECT (id, board_id, owner, body), INSERT (board_id, owner, body), UPDATE (body), DELETE ON mvis_notes TO e2e_mvis;
		ALTER TABLE mvis_notes ENABLE ROW LEVEL SECURITY;
		CREATE POLICY mvis_read ON mvis_notes FOR SELECT USING (true);
		CREATE POLICY mvis_insert ON mvis_notes FOR INSERT WITH CHECK (owner = NULLIF(current_setting('pdbq.claims.user_id', true), '')::integer);
		CREATE POLICY mvis_update ON mvis_notes FOR UPDATE USING (owner = NULLIF(current_setting('pdbq.claims.user_id', true), '')::integer);
		CREATE POLICY mvis_delete ON mvis_notes FOR DELETE USING (owner = NULLIF(current_setting('pdbq.claims.user_id', true), '')::integer);`)
	t.Cleanup(func() { execSQL(t, dbURL, `DROP TABLE IF EXISTS mvis_notes, mvis_boards`) })
	srv := testServer(t, func(c *config.Config) {
		c.RLS.Enabled = true
		c.RLS.Auth.Mode = "headers"
	})
	as1 := map[string]string{"X-Pdbr-Claim-Role": "e2e_mvis", "X-Pdbr-Claim-User-Id": "1"}
	call := func(method, path, body string, want int, v any) {
		t.Helper()
		res, out := send(t, method, srv.URL+path, body, as1)
		if res.StatusCode != want {
			t.Fatalf("%s %s = %d %s, want %d", method, path, res.StatusCode, out, want)
		}
		if v != nil {
			decode(t, out, v)
		}
	}
	bodies := func(r row) []string {
		t.Helper()
		var out []string
		for _, n := range embeddedRows(t, r, "mvis_boards", "mvis_notes") {
			out = append(out, fmt.Sprint(n["body"]))
		}
		sort.Strings(out)
		return out
	}

	var created row
	call("POST", "/api/mvis_notes?select=id,mvis_boards(mvis_notes(id,body))", `{"board_id":1,"owner":1,"body":"new"}`, 201, &created)
	if got := bodies(created); !slices.Equal(got, []string{"mine", "new", "theirs"}) {
		t.Errorf("create: embedded notes = %v", got)
	}

	var rows []row
	call("PATCH", "/api/mvis_notes?board_id=eq.1&select=id,body,mvis_boards(mvis_notes(id,body))", `{"body":"edited"}`, 200, &rows)
	if len(rows) != 2 {
		t.Fatalf("bulk patch must skip the other owner's note: %v", rows)
	}
	for _, r := range rows {
		if got := bodies(r); !slices.Equal(got, []string{"edited", "edited", "theirs"}) {
			t.Errorf("bulk patch: embedded notes = %v", got)
		}
	}

	res, out := send(t, "PATCH", srv.URL+"/api/mvis_notes?board_id=eq.1&select=id,mvis_boards(mvis_notes(secret))", `{"body":"x"}`, as1)
	wantProblem(t, res, out, 403, "pg:42501")

	call("DELETE", "/api/mvis_notes?board_id=eq.1&select=id,mvis_boards(mvis_notes(id,body))", "", 200, &rows)
	if len(rows) != 2 {
		t.Fatalf("bulk delete must skip the other owner's note: %v", rows)
	}
	for _, r := range rows {
		if got := bodies(r); !slices.Equal(got, []string{"theirs"}) {
			t.Errorf("bulk delete: embedded notes = %v", got)
		}
	}
}

func execSQL(t *testing.T, dbURL, sql string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, sql); err != nil {
		t.Fatal(err)
	}
}

func decode(t *testing.T, body []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("bad JSON %s: %v", body, err)
	}
}
