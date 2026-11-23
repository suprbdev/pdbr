package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbr/internal/config"
)

// The contrib suite creates its own schema (contribSchema) and installs
// citext, hstore, ltree and pg_trgm into contribExtSchema, which is not on
// the search_path, so every extension type, operator and function must be
// schema-qualified for the requests to work at all (and, for citext, to
// stay case-insensitive). Both schemas and the extensions this suite
// created are dropped afterwards.
const (
	contribSchema    = "pdbr_contrib"
	contribExtSchema = "pdbr_contrib_ext"
)

var contribExtensions = []string{"citext", "hstore", "ltree", "pg_trgm"}

// contribDDL creates kb-style docs: three rows covering every contrib type.
// search is generated from body; body has no index (filters.allow_columns
// admits it), the other columns carry the indexes their operators need.
const contribDDL = `
CREATE TABLE pdbr_contrib.docs (
  id     int PRIMARY KEY,
  title  text NOT NULL,
  body   text,
  search tsvector GENERATED ALWAYS AS (to_tsvector('english', coalesce(body, ''))) STORED,
  email  pdbr_contrib_ext.citext UNIQUE,
  attrs  pdbr_contrib_ext.hstore,
  path   pdbr_contrib_ext.ltree,
  paths  pdbr_contrib_ext.ltree[],
  pages  int4range,
  valid  daterange,
  slots  int4multirange,
  net    inet
);
CREATE INDEX docs_title_idx ON pdbr_contrib.docs USING gist (title pdbr_contrib_ext.gist_trgm_ops);
CREATE INDEX docs_search_idx ON pdbr_contrib.docs USING gin (search);
CREATE INDEX docs_attrs_idx ON pdbr_contrib.docs USING gin (attrs);
CREATE INDEX docs_path_idx ON pdbr_contrib.docs USING gist (path);
CREATE INDEX docs_paths_idx ON pdbr_contrib.docs USING gist (paths);
CREATE INDEX docs_pages_idx ON pdbr_contrib.docs USING gist (pages);
CREATE INDEX docs_valid_idx ON pdbr_contrib.docs USING gist (valid);
CREATE INDEX docs_slots_idx ON pdbr_contrib.docs USING gist (slots);
CREATE INDEX docs_net_idx ON pdbr_contrib.docs USING gist (net inet_ops);
INSERT INTO pdbr_contrib.docs (id, title, body, email, attrs, path, paths, pages, valid, slots, net) VALUES
  (1, 'PostgreSQL rocks', 'The fat cat sat on the mat', 'Ada@Example.com', 'color=>red, size=>L',
   'top.science.astronomy', '{top.science,top.art}', '[1,5)', '[2020-01-01,2020-02-01)', '{[1,3),[5,7)}', '10.1.2.3'),
  (2, 'Postgres tips', 'A fat rat ate the cheese', 'bob@example.com', 'color=>blue',
   'top.science.physics', '{top.hobbies}', '[10,20)', '[2021-01-01,2021-12-31]', '{[10,12)}', '192.168.1.10'),
  (3, 'MySQL notes', 'Dogs chase cats', 'carol@example.com', 'size=>S, note=>NULL',
   'top.art', '{}', 'empty', NULL, NULL, '10.0.0.0/8');
CREATE TABLE pdbr_contrib.authors (
  email pdbr_contrib_ext.citext PRIMARY KEY,
  name  text,
  meta  pdbr_contrib_ext.hstore,
  tags  pdbr_contrib_ext.hstore[]
);
CREATE INDEX authors_meta_idx ON pdbr_contrib.authors (meta);
CREATE TABLE pdbr_contrib.notes (
  id     int PRIMARY KEY,
  author pdbr_contrib_ext.citext REFERENCES pdbr_contrib.authors (email),
  trgm   text,
  near   text
);
CREATE INDEX notes_author_idx ON pdbr_contrib.notes (author);
CREATE INDEX notes_trgm_idx ON pdbr_contrib.notes (trgm);
CREATE INDEX notes_near_idx ON pdbr_contrib.notes (near);
INSERT INTO pdbr_contrib.authors VALUES ('Ada@Example.com', 'Ada', 'a=>1', NULL), ('bob@example.com', 'Bob', 'a=>2', NULL);
INSERT INTO pdbr_contrib.notes VALUES (1, 'ADA@example.com', 'x', 'n1'), (2, 'Bob@Example.com', 'y', 'n2');
CREATE FUNCTION pdbr_contrib.probe(h pdbr_contrib_ext.hstore, r int4range, hs pdbr_contrib_ext.hstore[])
RETURNS text LANGUAGE sql STABLE SET search_path = pdbr_contrib_ext, pg_catalog
AS $$ SELECT (h -> 'a') || ':' || lower(r)::text || ':' || coalesce(array_length(hs, 1), 0)::text $$;
`

// TestContribKeysAndJoins: citext keys, foreign keys and hstore sort values
// with the extension schema off the search_path, columns named like the
// soft-reserved trgm/near parameters, and the JSON forms of RPC arguments
// and array elements.
func TestContribKeysAndJoins(t *testing.T) {
	srv := contribServer(t, nil)
	var rows []row
	getJSON(t, srv+"/api/notes?select=id,authors(name)&order=id", &rows)
	if len(rows) != 2 || rows[0]["authors"] == nil || rows[1]["authors"] == nil {
		t.Errorf("forward embed over citext FK = %v", rows)
	}
	getJSON(t, srv+"/api/authors?select=email,notes(id)&order=email", &rows)
	for _, r := range rows {
		if n, _ := r["notes"].([]any); len(n) != 1 {
			t.Errorf("backward embed over citext FK = %v", rows)
		}
	}
	getJSON(t, srv+"/api/notes?select=id&order=authors(email).desc", &rows)
	if !eq(ids(rows, "id"), []int{2, 1}) {
		t.Errorf("related order by citext = %v", ids(rows, "id"))
	}

	var one row
	sendJSON(t, "PATCH", srv+"/api/authors/ADA@EXAMPLE.COM?select=name", `{"name": "Ada L"}`, 200, &one)
	if one["name"] != "Ada L" {
		t.Errorf("PATCH by citext key = %v", one)
	}
	sendJSON(t, "PATCH", srv+"/api/notes?author=eq.ada@EXAMPLE.com&select=id", `{"trgm": "z"}`, 200, &rows)
	if !eq(ids(rows, "id"), []int{1}) {
		t.Errorf("bulk PATCH by citext filter = %v", rows)
	}
	for col, want := range map[string]int{"trgm=eq.z": 1, "near=eq.n2": 2} {
		getJSON(t, srv+"/api/notes?select=id&"+col, &rows)
		if !eq(ids(rows, "id"), []int{want}) {
			t.Errorf("%s = %v, want [%d]", col, ids(rows, "id"), want)
		}
	}
	res, out := get(t, srv+"/api/docs?trgm=x")
	wantProblem(t, res, out, 400, "invalid_filter")

	var created row
	sendJSON(t, "POST", srv+"/api/authors?select=email,meta,tags", `{"email": "Cy@Example.com", "meta": {"a": "0"}, "tags": [{"k": "v"}, "x=>y"]}`, 201, &created)
	if want := (row{"email": "Cy@Example.com", "meta": row{"a": "0"}, "tags": []any{row{"k": "v"}, row{"x": "y"}}}); !reflect.DeepEqual(created, want) {
		t.Errorf("created = %v, want %v", created, want)
	}

	var full []row
	getJSON(t, srv+"/api/authors?select=email&order=meta.desc", &full)
	next := srv + "/api/authors?select=email&order=meta.desc&limit=1"
	var walked []any
	for range 5 {
		res := getJSON(t, next, &rows)
		for _, r := range rows {
			walked = append(walked, r["email"])
		}
		cursor := res.Header.Get("X-Next-Cursor")
		if cursor == "" {
			break
		}
		next = srv + "/api/authors?select=email&order=meta.desc&limit=1&after=" + cursor
	}
	var want []any
	for _, r := range full {
		want = append(want, r["email"])
	}
	if len(want) != 3 || !reflect.DeepEqual(walked, want) {
		t.Errorf("keyset walk by hstore = %v, want %v", walked, want)
	}

	sendJSON(t, "DELETE", srv+"/api/notes/2", "", 200, nil)
	sendJSON(t, "DELETE", srv+"/api/authors/BOB@EXAMPLE.COM", "", 200, nil)

	var got any
	sendJSON(t, "POST", srv+"/api/rpc/probe", `{"h": {"a": "v"}, "r": {"start": 3, "end": 5}, "hs": [{"k": "v"}, "a=>b"]}`, 200, &got)
	if got != "v:3:2" {
		t.Errorf("rpc probe with JSON args = %v", got)
	}
	getJSON(t, srv+"/api/rpc/probe?h="+url.QueryEscape("a=>w")+"&r="+url.QueryEscape("[4,9)")+"&hs="+url.QueryEscape(`{"a=>b"}`), &got)
	if got != "w:4:1" {
		t.Errorf("rpc probe with literal GET args = %v", got)
	}
}

// contribServer sets the contrib schema up (once per test, torn down by its
// own connection after the test) and serves it with docs.body, docs.attrs
// and docs.path in filters.allow_columns so the operators no index serves
// are reachable too.
func contribServer(t *testing.T, mutate func(*config.Config)) string {
	t.Helper()
	dbURL := os.Getenv("PDBR_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var created []string
	t.Cleanup(func() {
		c, err := pgx.Connect(ctx, dbURL)
		if err != nil {
			t.Errorf("cleanup: %v", err)
			return
		}
		defer c.Close(ctx)
		_, _ = c.Exec(ctx, `DROP SCHEMA IF EXISTS `+contribSchema+` CASCADE`)
		for _, ext := range created {
			_, _ = c.Exec(ctx, `DROP EXTENSION IF EXISTS `+ext+` CASCADE`)
		}
		_, _ = c.Exec(ctx, `DROP SCHEMA IF EXISTS `+contribExtSchema+` CASCADE`)
	})
	if _, err := conn.Exec(ctx, `DROP SCHEMA IF EXISTS `+contribSchema+` CASCADE; CREATE SCHEMA IF NOT EXISTS `+contribExtSchema+`; CREATE SCHEMA `+contribSchema); err != nil {
		t.Fatal(err)
	}
	for _, ext := range contribExtensions {
		var schema string
		err := conn.QueryRow(ctx, `SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace WHERE e.extname = $1`, ext).Scan(&schema)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if _, err := conn.Exec(ctx, `CREATE EXTENSION `+ext+` SCHEMA `+contribExtSchema); err != nil {
				t.Skipf("extension %s unavailable: %v", ext, err)
			}
			created = append(created, ext)
		case err != nil:
			t.Fatal(err)
		case schema != contribExtSchema:
			t.Skipf("extension %s is already installed in schema %s", ext, schema)
		}
	}
	if _, err := conn.Exec(ctx, contribDDL); err != nil {
		t.Fatal(err)
	}
	srv := testServer(t, func(cfg *config.Config) {
		cfg.Schema.Schemas = []string{contribSchema}
		cfg.Filters.AllowColumns = map[string][]string{contribSchema + ".docs": {"body", "attrs", "path"}}
		if mutate != nil {
			mutate(cfg)
		}
	})
	return srv.URL
}

// docsURL builds /api/docs?<params>, percent-encoding every value.
func docsURL(srv string, params ...string) string {
	var sb strings.Builder
	sb.WriteString(srv + "/api/docs?")
	for i, p := range params {
		if i > 0 {
			sb.WriteByte('&')
		}
		k, v, _ := strings.Cut(p, "=")
		sb.WriteString(k + "=" + url.QueryEscape(v))
	}
	return sb.String()
}

func TestContribFilters(t *testing.T) {
	srv := contribServer(t, nil)
	cases := []struct {
		params []string
		want   []int
	}{
		{[]string{"search=fts(english).fat & cat"}, []int{1}},
		{[]string{"search=fts.fat"}, []int{1, 2}},
		{[]string{"search=plfts(english).fat cat"}, []int{1}},
		{[]string{"search=phfts(english).fat cat"}, []int{1}},
		{[]string{"search=phfts(english).cat fat"}, nil},
		{[]string{"search=wfts(english).\"fat cat\" or dogs"}, []int{1, 3}},
		{[]string{"search=wfts(pg_catalog.english).fat -cat"}, []int{2}},
		{[]string{"search=not.fts(english).fat"}, []int{3}},
		{[]string{"body=fts(english).dog"}, []int{3}},
		{[]string{"body=plfts.cheese"}, []int{2}},
		{[]string{"or=(body.wfts(english).dogs,search.fts.rat)"}, []int{2, 3}},
		{[]string{"title=trgm_sim.postgres"}, []int{1, 2}},
		{[]string{"title=trgm_wsim.tips"}, []int{2}},
		{[]string{"title=trgm_swsim.notes"}, []int{3}},
		{[]string{"title=not.trgm_sim.postgres"}, []int{3}},
		{[]string{"email=eq.ADA@EXAMPLE.COM"}, []int{1}},
		{[]string{"email=neq.ada@example.com"}, []int{2, 3}},
		{[]string{"email=gt.B"}, []int{2, 3}},
		{[]string{"email=in.(CAROL@example.com,nobody@x)"}, []int{3}},
		{[]string{"email=like.ADA*"}, []int{1}},
		{[]string{"email=ilike.*EXAMPLE*"}, []int{1, 2, 3}},
		{[]string{"email=sw.BOB"}, []int{2}},
		{[]string{"email=ew.@EXAMPLE.COM"}, []int{1, 2, 3}},
		{[]string{"email=isdistinct.ADA@example.com"}, []int{2, 3}},
		{[]string{"email=not.isdistinct.ADA@example.com"}, []int{1}},
		{[]string{"attrs=hk.color"}, []int{1, 2}},
		{[]string{"attrs=hk_all.{color,size}"}, []int{1}},
		{[]string{"attrs=hk_any.{note,color}"}, []int{1, 2, 3}},
		{[]string{`attrs=cs.{"color":"red"}`}, []int{1}},
		{[]string{"attrs=cs.color=>blue"}, []int{2}},
		{[]string{`attrs=cd.{"color":"red","size":"L","x":"y"}`}, []int{1}},
		{[]string{"attrs=eq.size=>S, note=>NULL"}, []int{3}},
		{[]string{"attrs=in.(\"color=>blue\",\"a=>b\")"}, []int{2}},
		{[]string{"path=cs.top.science.physics"}, []int{2}},
		{[]string{"path=cd.top.science"}, []int{1, 2}},
		{[]string{"path=ancestor.top.science.astronomy.deep"}, []int{1}},
		{[]string{"path=descendant.top.science"}, []int{1, 2}},
		{[]string{"path=ancestor_any.{top.art.x,nowhere}"}, []int{3}},
		{[]string{"path=descendant_any.{top.art,top.science.physics}"}, []int{2, 3}},
		{[]string{"path=lquery.*.astronomy"}, []int{1}},
		{[]string{"path=lquery_any.{*.art,*.physics}"}, []int{2, 3}},
		{[]string{"path=ltxtquery.physics | art"}, []int{2, 3}},
		{[]string{"path=gt.top.b"}, []int{1, 2}},
		{[]string{"path=eq.top.art"}, []int{3}},
		{[]string{"paths=descendant.top.science"}, []int{1}},
		{[]string{"paths=lquery.top.*"}, []int{1, 2}},
		{[]string{"paths=cs.{top.hobbies}"}, []int{2}},
		{[]string{"pages=ov.[3,12)"}, []int{1, 2}},
		{[]string{"pages=cs.3"}, []int{1}},
		{[]string{"pages=cs.[11,12)"}, []int{2}},
		{[]string{`pages=cs.{"start":11,"end":13}`}, []int{2}},
		{[]string{"pages=cd.[0,100)"}, []int{1, 2, 3}},
		{[]string{"pages=sl.[6,7)"}, []int{1}},
		{[]string{"pages=sr.[6,7)"}, []int{2}},
		{[]string{"pages=nxr.[0,5)"}, []int{1}},
		{[]string{"pages=nxl.[5,100)"}, []int{2}},
		{[]string{"pages=adj.[5,10)"}, []int{1, 2}},
		{[]string{"pages=eq.empty"}, []int{3}},
		{[]string{"pages=not.ov.[3,12)"}, []int{3}},
		{[]string{"valid=cs.2020-01-15"}, []int{1}},
		{[]string{"valid=ov.[2021-06-01,)"}, []int{2}},
		{[]string{"valid=isdistinct.[2020-01-01,2020-02-01)"}, []int{2, 3}},
		{[]string{"slots=cs.6"}, []int{1}},
		{[]string{"slots=ov.[11,12)"}, []int{2}},
		{[]string{"slots=sl.[8,9)"}, []int{1}},
		{[]string{"net=cd.10.0.0.0/8"}, []int{1, 3}},
		{[]string{"net=cd_strict.10.0.0.0/8"}, []int{1}},
		{[]string{"net=cs.10.1.2.3"}, []int{1, 3}},
		{[]string{"net=cs_strict.10.1.2.3"}, []int{3}},
		{[]string{"net=ov.192.168.0.0/16"}, []int{2}},
		{[]string{"net=eq.10.1.2.3"}, []int{1}},
	}
	for _, c := range cases {
		params := append([]string{"select=id", "order=id"}, c.params...)
		var rows []row
		getJSON(t, docsURL(srv, params...), &rows)
		if got := ids(rows, "id"); !eq(got, c.want) {
			t.Errorf("%v = %v, want %v", c.params, got, c.want)
		}
	}
}

func TestContribOrder(t *testing.T) {
	srv := contribServer(t, nil)
	cases := []struct {
		params []string
		want   []int
	}{
		{[]string{"search=fts(english).cat | dog", "order=search.rank.desc"}, []int{3, 1}},
		{[]string{"search=fts(english).cat | dog", "order=search.rank"}, []int{1, 3}},
		{[]string{"body=wfts(english).cat or dog", "order=body.rank_cd.desc"}, []int{3, 1}},
		{[]string{"order=title.trgm_dist", "trgm=postgres"}, []int{2, 1, 3}},
		{[]string{"order=title.trgm_dist.desc", "trgm=postgres"}, []int{3, 1, 2}},
		{[]string{"order=title.trgm_wdist", "trgm=notes"}, []int{3, 2, 1}},
		{[]string{"title=trgm_sim.postgres", "order=title.trgm_dist,id", "trgm=postgresql"}, []int{1, 2}},
	}
	for _, c := range cases {
		var rows []row
		getJSON(t, docsURL(srv, append([]string{"select=id"}, c.params...)...), &rows)
		if got := ids(rows, "id"); !eq(got, c.want) {
			t.Errorf("%v = %v, want %v", c.params, got, c.want)
		}
	}

	next := docsURL(srv, "select=id", "order=email.desc", "limit=1")
	var seen []int
	for range 4 {
		var rows []row
		res := getJSON(t, next, &rows)
		seen = append(seen, ids(rows, "id")...)
		cursor := res.Header.Get("X-Next-Cursor")
		if cursor == "" {
			break
		}
		next = docsURL(srv, "select=id", "order=email.desc", "limit=1", "after="+cursor)
	}
	if !eq(seen, []int{3, 2, 1}) {
		t.Errorf("keyset walk by email desc = %v, want [3 2 1]", seen)
	}

	var row1 row
	getJSON(t, srv+"/api/docs/by/email/"+url.PathEscape("ADA@example.COM")+"?select=id", &row1)
	if row1["id"] != float64(1) {
		t.Errorf("unique lookup by citext = %v", row1)
	}
}

func TestContribOutputAndMutations(t *testing.T) {
	srv := contribServer(t, nil)
	var rows []row
	getJSON(t, docsURL(srv, "select=id,attrs,pages,valid,slots,net,path,paths,email", "order=id"), &rows)
	want := []row{
		{"id": 1.0, "attrs": row{"color": "red", "size": "L"}, "pages": "[1,5)", "valid": "[2020-01-01,2020-02-01)", "slots": "{[1,3),[5,7)}",
			"net": "10.1.2.3", "path": "top.science.astronomy", "paths": []any{"top.science", "top.art"}, "email": "Ada@Example.com"},
		{"id": 2.0, "attrs": row{"color": "blue"}, "pages": "[10,20)", "valid": "[2021-01-01,2022-01-01)", "slots": "{[10,12)}",
			"net": "192.168.1.10", "path": "top.science.physics", "paths": []any{"top.hobbies"}, "email": "bob@example.com"},
		{"id": 3.0, "attrs": row{"size": "S", "note": nil}, "pages": "empty", "valid": nil, "slots": nil,
			"net": "10.0.0.0/8", "path": "top.art", "paths": []any{}, "email": "carol@example.com"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("output =\n%v\nwant\n%v", rows, want)
	}

	body := `{"id": 4, "title": "new", "body": "fresh doc", "email": "Dan@Example.com",
		"attrs": {"k": "v", "n": null}, "path": "top.new", "paths": ["a.b"],
		"pages": {"start": 1, "end": 3, "endInclusive": true}, "valid": {"start": "2022-01-01", "end": null},
		"slots": [{"start": 1, "end": 2}, "[4,6)"], "net": "10.9.9.9"}`
	var created row
	sendJSON(t, "POST", srv+"/api/docs?select=id,attrs,pages,valid,slots,search", body, 201, &created)
	wantCreated := row{"id": 4.0, "attrs": row{"k": "v", "n": nil}, "pages": "[1,4)", "valid": "[2022-01-01,)", "slots": "{[1,2),[4,6)}", "search": "'doc':2 'fresh':1"}
	if !reflect.DeepEqual(created, wantCreated) {
		t.Errorf("created = %v, want %v", created, wantCreated)
	}
	var patched row
	sendJSON(t, "PATCH", srv+"/api/docs/4?select=attrs,pages,valid,slots", `{"attrs": "a=>b", "pages": "empty", "valid": {"isEmpty": true}, "slots": null}`, 200, &patched)
	if want := (row{"attrs": row{"a": "b"}, "pages": "empty", "valid": "empty", "slots": nil}); !reflect.DeepEqual(patched, want) {
		t.Errorf("patched = %v, want %v", patched, want)
	}
	for _, bad := range []string{`{"attrs": {"a": 1}}`, `{"pages": {"start": 1.5}}`, `{"slots": {"start": 1}}`, `{"attrs": [1]}`} {
		res, out := send(t, "PATCH", srv+"/api/docs/4", bad, nil)
		wantProblem(t, res, out, 400, "invalid_body")
	}
	sendJSON(t, "DELETE", srv+"/api/docs/4", "", 200, nil)
}

func TestContribErrors(t *testing.T) {
	srv := contribServer(t, nil)
	for _, c := range []struct {
		params []string
		code   string
	}{
		{[]string{"title=fts.x"}, "column_not_filterable"},
		{[]string{"paths=ancestor.top"}, "column_not_filterable"},
		{[]string{"id=fts.x"}, "invalid_filter"},
		{[]string{"pages=cs.x"}, "invalid_filter"},
		{[]string{"attrs=gt.a=>1"}, "invalid_filter"},
		{[]string{"attrs=cs.{\"a\":1}"}, "invalid_filter"},
		{[]string{"order=title.trgm_dist"}, "invalid_filter"},
		{[]string{"order=search.rank"}, "invalid_filter"},
		{[]string{"order=email.trgm_dist", "trgm=x"}, "column_not_orderable"},
		{[]string{"search=fts(english.x"}, "invalid_filter"},
		{[]string{"search=fts.fat cat"}, "pg:42601"},
		{[]string{"search=fts(klingon).x"}, "pg:42704"},
		{[]string{"path=descendant.a..b"}, "pg:42601"},
	} {
		res, out := get(t, docsURL(srv, c.params...))
		wantProblem(t, res, out, 400, c.code)
	}
}

// TestContribOpenAPI checks the document against the 3.1 meta-schema and
// runs every documented operator of every filterable column against the
// database: x-pdbr-filter-ops lists exactly what parses (unit-tested) and
// all of it executes.
func TestContribOpenAPI(t *testing.T) {
	for _, indexedOnly := range []bool{true, false} {
		srv := contribServer(t, func(cfg *config.Config) { cfg.Filters.IndexedOnly = indexedOnly })
		body, doc := fetchOpenAPI(t, srv)
		validateOpenAPI(t, oasSchema(t), body)
		params := opParams(pathOp(doc, "/docs", "get"))
		if params["query:trgm"] == nil {
			t.Error("trgm param missing")
		}
		order, _ := params["query:order"]["description"].(string)
		if !strings.Contains(order, "search.rank") || !strings.Contains(order, "title.trgm_dist") {
			t.Errorf("order description = %s", order)
		}
		samples := map[string]map[string]string{
			"id":     {"": "1"},
			"title":  {"": "x"},
			"body":   {"": "x"},
			"search": {"": "fat"},
			"email":  {"": "ada@example.com"},
			"attrs":  {"": "a=>1", "cs": `{"a":"1"}`, "cd": `{"a":"1"}`, "hk": "a", "hk_all": "{a}", "hk_any": "{a}"},
			"path":   {"": "top", "ancestor_any": "{top}", "descendant_any": "{top}", "lquery": "top.*", "lquery_any": "{top.*}", "ltxtquery": "top"},
			"paths":  {"": "{top}", "descendant": "top", "ancestor": "top", "lquery": "top.*", "lquery_any": "{top.*}", "ltxtquery": "top"},
			"pages":  {"": "[1,2)", "cs": "1"},
			"valid":  {"": "[2020-01-01,2020-01-02)"},
			"slots":  {"": "{[1,2)}", "ov": "[1,2)", "cs": "1", "cd": "[1,2)", "sl": "[1,2)", "sr": "[1,2)", "nxr": "[1,2)", "nxl": "[1,2)", "adj": "[1,2)"},
			"net":    {"": "10.0.0.0/8"},
		}
		n := 0
		for name, p := range params {
			col, ok := strings.CutPrefix(name, "query:")
			if !ok || samples[col] == nil {
				continue
			}
			ops, _ := p["x-pdbr-filter-ops"].([]any)
			for _, o := range ops {
				op := o.(string)
				v, ok := samples[col][op]
				if !ok {
					v = samples[col][""]
				}
				switch op {
				case "is":
					v = "null"
				case "in":
					v = `("` + v + `")`
				}
				res, out := get(t, docsURL(srv, "select=id", col+"="+op+"."+v))
				if res.StatusCode != 200 {
					t.Errorf("indexed_only=%v %s=%s.%s = %d %s", indexedOnly, col, op, v, res.StatusCode, out)
				}
				n++
			}
		}
		if n < 100 {
			t.Errorf("indexed_only=%v: only %d documented operators exercised", indexedOnly, n)
		}
		raw, _ := json.Marshal(componentSchema(doc, "docs.insert")["properties"].(map[string]any)["pages"])
		if !strings.Contains(string(raw), "startInclusive") {
			t.Errorf("docs.insert pages = %s", raw)
		}
	}
}
