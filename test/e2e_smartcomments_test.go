package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbr/internal/config"
)

// smartFixture creates a small commented schema (prefix sc_) next to the
// shared fixture and drops it again at the end of the test. Every test in
// this file boots its own server, so the comments only ever affect the
// catalog introspected inside the test.
func smartFixture(t *testing.T) {
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
		for _, ddl := range []string{
			`DROP VIEW IF EXISTS public.sc_note_stats`,
			`DROP FUNCTION IF EXISTS public.sc_notes_shout(public.sc_notes)`,
			`DROP FUNCTION IF EXISTS public.sc_hello()`,
			`DROP FUNCTION IF EXISTS public.sc_ping()`,
			`DROP TABLE IF EXISTS public.sc_tags`,
			`DROP TABLE IF EXISTS public.sc_notes`,
			`DROP TYPE IF EXISTS public.sc_level`,
		} {
			if _, err := conn.Exec(ctx, ddl); err != nil {
				t.Errorf("smart fixture cleanup: %v", err)
			}
		}
	}
	drop()
	t.Cleanup(func() {
		drop()
		conn.Close(ctx)
	})
	for _, ddl := range []string{
		`CREATE TYPE public.sc_level AS ENUM ('lo', 'hi')`,
		`COMMENT ON TYPE public.sc_level IS E'@name level\nHow loud.'`,
		`CREATE TABLE public.sc_notes (
		    id         integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		    title      text NOT NULL,
		    body       text,
		    secret     text,
		    level      public.sc_level NOT NULL DEFAULT 'lo',
		    created_at timestamptz NOT NULL DEFAULT now())`,
		`COMMENT ON TABLE public.sc_notes IS E'@name notes\n@omit delete\nA note.'`,
		`COMMENT ON COLUMN public.sc_notes.title IS E'@filterable\nThe title.'`,
		`COMMENT ON COLUMN public.sc_notes.body IS '@deprecated use title'`,
		`COMMENT ON COLUMN public.sc_notes.secret IS '@omit'`,
		`COMMENT ON COLUMN public.sc_notes.created_at IS '@omit create'`,
		`CREATE TABLE public.sc_tags (
		    id      integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		    note_id integer NOT NULL REFERENCES public.sc_notes (id),
		    label   text NOT NULL UNIQUE)`,
		`COMMENT ON TABLE public.sc_tags IS '@omit all'`,
		`COMMENT ON CONSTRAINT sc_tags_note_id_fkey ON public.sc_tags IS E'@fieldName note\n@foreignFieldName labels'`,
		`CREATE VIEW public.sc_note_stats AS
		    SELECT id AS note_id, title, length(coalesce(body, '')) AS body_len FROM public.sc_notes`,
		`COMMENT ON VIEW public.sc_note_stats IS
		    E'@primaryKey note_id\n@foreignKey (note_id) references sc_notes (id)|@fieldName note|@foreignFieldName stats'`,
		`CREATE FUNCTION public.sc_notes_shout(n public.sc_notes) RETURNS text STABLE LANGUAGE sql AS $$
		    SELECT upper(n.title) $$`,
		`COMMENT ON FUNCTION public.sc_notes_shout(public.sc_notes) IS '@deprecated shouting is rude'`,
		`CREATE FUNCTION public.sc_hello() RETURNS text STABLE LANGUAGE sql AS $$ SELECT 'hello' $$`,
		`COMMENT ON FUNCTION public.sc_hello() IS '@omit'`,
		`CREATE FUNCTION public.sc_ping() RETURNS text STABLE LANGUAGE sql AS $$ SELECT 'pong' $$`,
		`COMMENT ON FUNCTION public.sc_ping() IS '@name sc_pong'`,
		`INSERT INTO public.sc_notes (title, body, secret) VALUES ('first', 'b1', 's1'), ('second', NULL, 's2')`,
		`INSERT INTO public.sc_tags (note_id, label) VALUES (1, 'a'), (1, 'b'), (2, 'c')`,
	} {
		if _, err := conn.Exec(ctx, ddl); err != nil {
			t.Fatalf("smart fixture: %v\n%s", err, ddl)
		}
	}
}

func openAPIDoc(t *testing.T, srv string) map[string]any {
	t.Helper()
	var doc map[string]any
	getJSON(t, srv+"/api/openapi.json", &doc)
	return doc
}

func schemaOf(doc map[string]any, name string) map[string]any {
	s, _ := doc["components"].(map[string]any)["schemas"].(map[string]any)[name].(map[string]any)
	return s
}

func TestSmartCommentsRoutesAndMethods(t *testing.T) {
	smartFixture(t)
	srv := testServer(t, nil).URL

	var rows []row
	getJSON(t, srv+"/api/notes", &rows)
	if len(rows) != 2 || rows[0]["title"] != "first" {
		t.Fatalf("notes = %v", rows)
	}
	if _, ok := rows[0]["secret"]; ok {
		t.Fatalf("@omit column in response: %v", rows[0])
	}
	if _, ok := rows[0]["shout"]; ok {
		t.Fatalf("computed column selected by default: %v", rows[0])
	}
	res, body := get(t, srv+"/api/sc_notes")
	wantProblem(t, res, body, 404, "not_found")

	res, body = do(t, http.MethodDelete, srv+"/api/notes/1", nil)
	wantProblem(t, res, body, 405, "method_not_allowed")
	if allow := res.Header.Get("Allow"); allow != "GET, HEAD, PATCH" {
		t.Fatalf("Allow = %q", allow)
	}

	// @omit all: the collection keeps its write methods, rows stay readable.
	res, body = get(t, srv+"/api/sc_tags")
	wantProblem(t, res, body, 405, "method_not_allowed")
	if allow := res.Header.Get("Allow"); allow != "POST, PUT" {
		t.Fatalf("sc_tags Allow = %q", allow)
	}
	var tag row
	getJSON(t, srv+"/api/sc_tags/1", &tag)
	if tag["label"] != "a" {
		t.Fatalf("tag = %v", tag)
	}
	getJSON(t, srv+"/api/sc_tags/by/label/c", &tag)
	if tag["note_id"] != float64(2) {
		t.Fatalf("tag by label = %v", tag)
	}

	// @primaryKey on the view: row path and keyset cursors.
	var stat row
	getJSON(t, srv+"/api/sc_note_stats/1", &stat)
	if stat["body_len"] != float64(2) {
		t.Fatalf("stat = %v", stat)
	}
	res = getJSON(t, srv+"/api/sc_note_stats?limit=1&order=note_id", &rows)
	if res.Header.Get("X-Next-Cursor") == "" {
		t.Fatal("no keyset cursor on the @primaryKey view")
	}

	// Functions: @omit hides the RPC, @name renames it, @deprecated keeps it.
	res, body = get(t, srv+"/api/rpc/sc_hello")
	wantProblem(t, res, body, 404, "not_found")
	var pong string
	getJSON(t, srv+"/api/rpc/sc_pong", &pong)
	if pong != "pong" {
		t.Fatalf("pong = %q", pong)
	}
	getJSON(t, srv+"/api/notes?select=id,shout&id=eq.1", &rows)
	if len(rows) != 1 || rows[0]["shout"] != "FIRST" {
		t.Fatalf("shout = %v", rows)
	}
}

func TestSmartCommentsColumnsAndBodies(t *testing.T) {
	smartFixture(t)
	srv := testServer(t, nil).URL

	res, body := get(t, srv+"/api/notes?select=id,secret")
	wantProblem(t, res, body, 400, "unknown_column")
	res, body = get(t, srv+"/api/notes?secret=eq.s1")
	wantProblem(t, res, body, 400, "unknown_column")

	// @filterable admits an unindexed column; body stays under indexed_only.
	var rows []row
	getJSON(t, srv+"/api/notes?title=eq.second&order=title.desc", &rows)
	if len(rows) != 1 || rows[0]["id"] != float64(2) {
		t.Fatalf("filter by title = %v", rows)
	}
	res, body = get(t, srv+"/api/notes?body=eq.b1")
	wantProblem(t, res, body, 400, "column_not_filterable")

	res, body = send(t, http.MethodPost, srv+"/api/notes", `{"title":"x","secret":"s"}`, nil)
	wantProblem(t, res, body, 400, "invalid_body")
	res, body = send(t, http.MethodPost, srv+"/api/notes", `{"title":"x","created_at":"2020-01-01T00:00:00Z"}`, nil)
	p := wantProblem(t, res, body, 400, "invalid_body")
	if !strings.Contains(p.Detail, `"created_at" of "notes" cannot be written by POST`) {
		t.Fatalf("detail = %q", p.Detail)
	}
	var created row
	sendJSON(t, http.MethodPost, srv+"/api/notes", `{"title":"third","level":"hi"}`, 201, &created)
	if created["level"] != "hi" || created["id"] != float64(3) {
		t.Fatalf("created = %v", created)
	}
	// @omit create does not touch PATCH.
	var patched row
	sendJSON(t, http.MethodPatch, srv+"/api/notes/3", `{"created_at":"2020-01-01T00:00:00Z"}`, 200, &patched)
	if !strings.HasPrefix(patched["created_at"].(string), "2020-01-01") {
		t.Fatalf("patched = %v", patched)
	}
}

func TestSmartCommentsEmbeds(t *testing.T) {
	smartFixture(t)
	srv := testServer(t, nil).URL

	var rows []row
	getJSON(t, srv+"/api/notes?select=id,labels(label),stats(body_len)&order=id", &rows)
	if len(rows) != 2 {
		t.Fatalf("rows = %v", rows)
	}
	labels := rows[0]["labels"].([]any)
	if len(labels) != 2 || rows[0]["stats"].([]any)[0].(map[string]any)["body_len"] != float64(2) {
		t.Fatalf("row 1 = %v", rows[0])
	}
	var tag row
	getJSON(t, srv+"/api/sc_tags/1?select=id,note(title)", &tag)
	if tag["note"].(map[string]any)["title"] != "first" {
		t.Fatalf("tag = %v", tag)
	}
	var stat row
	getJSON(t, srv+"/api/sc_note_stats/2?select=note_id,note(title)", &stat)
	if stat["note"].(map[string]any)["title"] != "second" {
		t.Fatalf("stat = %v", stat)
	}
	// The default table-name keys are gone once @fieldName names them.
	res, body := get(t, srv+"/api/sc_tags/1?select=sc_notes(*)")
	wantProblem(t, res, body, 400, "unknown_relation")
	res, body = get(t, srv+"/api/notes?select=sc_tags(*)")
	wantProblem(t, res, body, 400, "unknown_relation")
}

func TestSmartCommentsOpenAPI(t *testing.T) {
	smartFixture(t)
	srv := testServer(t, nil).URL
	doc := openAPIDoc(t, srv)
	raw, _ := json.Marshal(doc)
	if strings.Contains(string(raw), "@") {
		t.Fatalf("tag line leaked into openapi: %s", raw)
	}
	notes := schemaOf(doc, "notes")
	if notes == nil || schemaOf(doc, "sc_notes") != nil {
		t.Fatalf("resource schema not renamed: %v", schemaOf(doc, "sc_notes"))
	}
	if notes["description"] != "A note." {
		t.Fatalf("description = %v", notes["description"])
	}
	props := notes["properties"].(map[string]any)
	if _, ok := props["secret"]; ok {
		t.Fatal("secret in openapi")
	}
	if p := props["body"].(map[string]any); p["deprecated"] != true || p["x-deprecation-reason"] != "use title" {
		t.Fatalf("body property = %v", p)
	}
	if p := props["title"].(map[string]any); p["description"] != "The title." {
		t.Fatalf("title property = %v", p)
	}
	if p := props["shout"].(map[string]any); p["deprecated"] != true || p["x-deprecation-reason"] != "shouting is rude" {
		t.Fatalf("shout property = %v", p)
	}
	if p := props["level"].(map[string]any); p["$ref"] != "#/components/schemas/level" {
		t.Fatalf("level property = %v", p)
	}
	if e := schemaOf(doc, "level"); e == nil || e["description"] != "How loud." || schemaOf(doc, "sc_level") != nil {
		t.Fatalf("enum schema = %v", e)
	}
	if _, ok := props["labels"]; !ok {
		t.Fatalf("labels embed missing: %v", props)
	}
	if _, ok := props["stats"]; !ok {
		t.Fatalf("stats embed missing: %v", props)
	}
	paths := doc["paths"].(map[string]any)
	for _, want := range []string{"/notes", "/notes/{id}", "/sc_tags/{id}", "/sc_tags/by/label/{label}", "/sc_note_stats/{note_id}", "/rpc/sc_pong"} {
		if paths[want] == nil {
			t.Errorf("path %s missing", want)
		}
	}
	for _, absent := range []string{"/sc_notes", "/rpc/sc_hello", "/rpc/sc_ping"} {
		if paths[absent] != nil {
			t.Errorf("path %s present", absent)
		}
	}
	// @omit all keeps the collection path for its write methods, minus GET/HEAD.
	if item := paths["/sc_tags"].(map[string]any); item["get"] != nil || item["head"] != nil {
		t.Errorf("/sc_tags still documents reads: %v", item)
	}
	// The table comment leads the operation description (the advanced-filters
	// prose follows after a blank line).
	if op := paths["/notes"].(map[string]any)["get"].(map[string]any); !strings.HasPrefix(op["description"].(string), "A note.") {
		t.Fatalf("list operation description = %v", op["description"])
	}
}

func TestSmartCommentsDisabled(t *testing.T) {
	smartFixture(t)
	srv := testServer(t, func(c *config.Config) {
		c.Plugins.Disabled = []string{"smart-comments"}
	}).URL

	var rows []row
	getJSON(t, srv+"/api/sc_notes?select=id,secret,sc_tags(label)&order=id", &rows)
	if len(rows) != 2 || rows[0]["secret"] != "s1" || len(rows[0]["sc_tags"].([]any)) != 2 {
		t.Fatalf("raw surface = %v", rows)
	}
	res, body := get(t, srv+"/api/notes")
	wantProblem(t, res, body, 404, "not_found")
	res, body = get(t, srv+"/api/sc_notes?select=labels(label)")
	wantProblem(t, res, body, 400, "unknown_relation")
	res, body = get(t, srv+"/api/sc_notes?title=eq.first")
	wantProblem(t, res, body, 400, "column_not_filterable")
	getJSON(t, srv+"/api/sc_tags", &rows)
	res, body = get(t, srv+"/api/sc_note_stats/1")
	wantProblem(t, res, body, 404, "not_found")
	var hello string
	getJSON(t, srv+"/api/rpc/sc_hello", &hello)
	if hello != "hello" {
		t.Fatalf("hello = %q", hello)
	}
	res, body = do(t, http.MethodDelete, srv+"/api/sc_notes/999", nil)
	if res.StatusCode != 404 {
		t.Fatalf("DELETE without the plugin = %d %s (want 404: method allowed, row missing)", res.StatusCode, body)
	}

	doc := openAPIDoc(t, srv)
	if schemaOf(doc, "notes") != nil || schemaOf(doc, "sc_notes") == nil {
		t.Fatal("openapi still renamed")
	}
	props := schemaOf(doc, "sc_notes")["properties"].(map[string]any)
	if _, ok := props["secret"]; !ok {
		t.Fatal("secret missing without the plugin")
	}
	if p := props["body"].(map[string]any); p["deprecated"] == true {
		t.Fatal("deprecated without the plugin")
	}
	if schemaOf(doc, "sc_notes")["description"] != "A note." {
		t.Fatalf("description stripping is core: %v", schemaOf(doc, "sc_notes")["description"])
	}
	if schemaOf(doc, "sc_level") == nil || schemaOf(doc, "level") != nil {
		t.Fatal("enum renamed without the plugin")
	}
	paths := doc["paths"].(map[string]any)
	if paths["/sc_note_stats/{note_id}"] != nil || paths["/sc_notes"] == nil || paths["/rpc/sc_hello"] == nil {
		t.Fatal("paths do not reflect the raw surface")
	}
}
