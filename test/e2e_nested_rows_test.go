package e2e

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/suprbdev/pdbcore/confx"

	"github.com/suprbdev/pdbr/internal/config"
)

const (
	nestedSchema = "nested_e2e"
	nestedPriv   = "nested_e2e_priv"
)

// nestedDDL: secrets is omitted, csecrets hidden from the public contract
// and priv.stash lives outside schema.schemas; wrap, cwrap and fwrap hold
// one of their rows each, secret_dom is a domain over secrets. docs has
// two @omit read columns, a raster column and an @omit read raster column,
// and its title is hidden from the public contract; dwrap holds a docs
// row, an array of them and a raster, doc_dom is a domain over docs, and
// dholder stores all of them next to a point.
const nestedDDL = `
CREATE TABLE nested_e2e.secrets (id int PRIMARY KEY, owner text, password text);
COMMENT ON TABLE nested_e2e.secrets IS '@omit';
INSERT INTO nested_e2e.secrets VALUES (1, 'alice', 's3cr3t-alice'), (2, 'bob', 's3cr3t-bob');
CREATE TABLE nested_e2e.csecrets (id int PRIMARY KEY, token text);
COMMENT ON TABLE nested_e2e.csecrets IS '@behavior -contract:public';
INSERT INTO nested_e2e.csecrets VALUES (1, 'ctoken-1');
CREATE TABLE nested_e2e_priv.stash (id int PRIMARY KEY, loot text);
INSERT INTO nested_e2e_priv.stash VALUES (1, 'LOOT-1');

CREATE TYPE nested_e2e.wrap AS (label text, s nested_e2e.secrets);
CREATE TYPE nested_e2e.wraps AS (label text, w nested_e2e.wrap[]);
CREATE TYPE nested_e2e.cwrap AS (label text, c nested_e2e.csecrets);
CREATE TYPE nested_e2e.fwrap AS (label text, st nested_e2e_priv.stash);
CREATE DOMAIN nested_e2e.secret_dom AS nested_e2e.secrets;
CREATE FUNCTION nested_e2e.get_wrap() RETURNS nested_e2e.wrap LANGUAGE sql STABLE AS $$ SELECT ('x', s)::nested_e2e.wrap FROM nested_e2e.secrets s ORDER BY id LIMIT 1 $$;
CREATE FUNCTION nested_e2e.get_wraps() RETURNS SETOF nested_e2e.wrap LANGUAGE sql STABLE AS $$ SELECT ('x', s)::nested_e2e.wrap FROM nested_e2e.secrets s $$;
CREATE FUNCTION nested_e2e.get_deep() RETURNS nested_e2e.wraps LANGUAGE sql STABLE AS $$ SELECT ('d', array_agg(('x', s)::nested_e2e.wrap))::nested_e2e.wraps FROM nested_e2e.secrets s $$;
CREATE FUNCTION nested_e2e.get_secret_dom() RETURNS nested_e2e.secret_dom LANGUAGE sql STABLE AS $$ SELECT s::nested_e2e.secret_dom FROM nested_e2e.secrets s ORDER BY id LIMIT 1 $$;
CREATE FUNCTION nested_e2e.get_secret_doms() RETURNS nested_e2e.secret_dom[] LANGUAGE sql STABLE AS $$ SELECT array_agg(s::nested_e2e.secret_dom) FROM nested_e2e.secrets s $$;
CREATE FUNCTION nested_e2e.get_cwrap() RETURNS nested_e2e.cwrap LANGUAGE sql STABLE AS $$ SELECT ('c', c)::nested_e2e.cwrap FROM nested_e2e.csecrets c LIMIT 1 $$;
CREATE FUNCTION nested_e2e.get_fwrap() RETURNS nested_e2e.fwrap LANGUAGE sql STABLE AS $$ SELECT ('f', s)::nested_e2e.fwrap FROM nested_e2e_priv.stash s LIMIT 1 $$;
CREATE FUNCTION nested_e2e.secret_tab() RETURNS TABLE (n int, w nested_e2e.wrap) LANGUAGE sql STABLE AS $$ SELECT 1, ('t', s)::nested_e2e.wrap FROM nested_e2e.secrets s $$;
CREATE TABLE nested_e2e.holder (id int PRIMARY KEY, w nested_e2e.wrap, sd nested_e2e.secret_dom, f nested_e2e.fwrap, cw nested_e2e.cwrap);
INSERT INTO nested_e2e.holder SELECT s.id, ('h', s)::nested_e2e.wrap, s::nested_e2e.secret_dom, NULL, NULL FROM nested_e2e.secrets s;
UPDATE nested_e2e.holder SET f = (SELECT ('f', st)::nested_e2e.fwrap FROM nested_e2e_priv.stash st), cw = (SELECT ('c', c)::nested_e2e.cwrap FROM nested_e2e.csecrets c);

CREATE TABLE nested_e2e.docs (id int PRIMARY KEY, title text, secret text, hidden_meta jsonb, rast raster, secret_rast raster);
COMMENT ON COLUMN nested_e2e.docs.secret IS '@omit read';
COMMENT ON COLUMN nested_e2e.docs.hidden_meta IS '@omit read';
COMMENT ON COLUMN nested_e2e.docs.secret_rast IS '@omit read';
COMMENT ON COLUMN nested_e2e.docs.title IS '@behavior -contract:public';
INSERT INTO nested_e2e.docs
SELECT i, 'doc ' || i, 'topsecret password ' || i, '{"pw": "hunter2 secret"}',
       ST_AddBand(ST_MakeEmptyRaster(2, 2, 0, 2, 1, -1, 0, 0, 4326), 1, '8BUI', 99, 0),
       ST_AddBand(ST_MakeEmptyRaster(2, 2, 0, 2, 1, -1, 0, 0, 4326), 1, '8BUI', 77, 0)
FROM generate_series(1, 3) i;
CREATE FUNCTION nested_e2e.wrap_label(w nested_e2e.wrap) RETURNS text LANGUAGE sql STABLE AS $$ SELECT w.label $$;
CREATE FUNCTION nested_e2e.docs_wrap(d nested_e2e.docs) RETURNS nested_e2e.wrap LANGUAGE sql STABLE AS $$ SELECT ('x', s)::nested_e2e.wrap FROM nested_e2e.secrets s ORDER BY id LIMIT 1 $$;

CREATE TYPE nested_e2e.dwrap AS (label text, d nested_e2e.docs, ds nested_e2e.docs[], r raster);
CREATE DOMAIN nested_e2e.doc_dom AS nested_e2e.docs;
CREATE FUNCTION nested_e2e.one_doc() RETURNS nested_e2e.docs LANGUAGE sql STABLE AS $$ SELECT * FROM nested_e2e.docs WHERE id = 1 $$;
CREATE FUNCTION nested_e2e.docs_arr() RETURNS nested_e2e.docs[] LANGUAGE sql STABLE AS $$ SELECT array_agg(d ORDER BY id) FROM nested_e2e.docs d WHERE id < 3 $$;
CREATE FUNCTION nested_e2e.docs_tab() RETURNS TABLE (n int, d nested_e2e.docs, r raster) LANGUAGE sql STABLE AS $$ SELECT d.id, d, d.rast FROM nested_e2e.docs d WHERE id < 2 $$;
CREATE FUNCTION nested_e2e.docs_self(d nested_e2e.docs) RETURNS nested_e2e.docs LANGUAGE sql STABLE AS $$ SELECT d $$;
CREATE FUNCTION nested_e2e.docs_selfarr(d nested_e2e.docs) RETURNS nested_e2e.docs[] LANGUAGE sql STABLE AS $$ SELECT ARRAY[d] $$;
CREATE FUNCTION nested_e2e.docs_boxes(d nested_e2e.docs) RETURNS SETOF nested_e2e.dwrap LANGUAGE sql STABLE AS $$ SELECT 'b', d, ARRAY[d], d.rast $$;
CREATE FUNCTION nested_e2e.docs_dwrap() RETURNS nested_e2e.dwrap LANGUAGE sql STABLE AS $$ SELECT 'w', d, ARRAY[d], d.rast FROM nested_e2e.docs d WHERE id = 1 $$;
CREATE FUNCTION nested_e2e.docs_dwraps() RETURNS SETOF nested_e2e.dwrap LANGUAGE sql STABLE AS $$ SELECT 'w', d, ARRAY[d], d.rast FROM nested_e2e.docs d ORDER BY id $$;
CREATE FUNCTION nested_e2e.doc_dom() RETURNS nested_e2e.doc_dom LANGUAGE sql STABLE AS $$ SELECT d::nested_e2e.doc_dom FROM nested_e2e.docs d WHERE id = 1 $$;
CREATE FUNCTION nested_e2e.doc_doms() RETURNS SETOF nested_e2e.doc_dom LANGUAGE sql STABLE AS $$ SELECT d::nested_e2e.doc_dom FROM nested_e2e.docs d ORDER BY id $$;
CREATE FUNCTION nested_e2e.get_rast() RETURNS raster LANGUAGE sql STABLE AS $$ SELECT rast FROM nested_e2e.docs WHERE id = 1 $$;
CREATE TABLE nested_e2e.dholder (id int PRIMARY KEY, doc_id int REFERENCES nested_e2e.docs (id), note text, geom geometry(Point, 4326),
  dw nested_e2e.dwrap, darr nested_e2e.docs[], d nested_e2e.docs, dd nested_e2e.doc_dom);
INSERT INTO nested_e2e.dholder SELECT d.id, d.id, 'n', ST_SetSRID(ST_MakePoint(d.id, d.id), 4326), ('h', d, ARRAY[d], d.rast)::nested_e2e.dwrap, ARRAY[d], d, d::nested_e2e.doc_dom
FROM nested_e2e.docs d WHERE id < 3;
`

// nestedSetup creates nestedDDL (installing postgis_raster when missing,
// dropped again afterwards) and returns the database URL.
func nestedSetup(t *testing.T) string {
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
	drop := `DROP SCHEMA IF EXISTS ` + nestedSchema + ` CASCADE; DROP SCHEMA IF EXISTS ` + nestedPriv + ` CASCADE;`
	created := false
	t.Cleanup(func() {
		c, err := pgx.Connect(ctx, dbURL)
		if err != nil {
			t.Errorf("cleanup: %v", err)
			return
		}
		defer c.Close(ctx)
		_, _ = c.Exec(ctx, drop)
		if created {
			_, _ = c.Exec(ctx, `DROP EXTENSION IF EXISTS postgis_raster CASCADE`)
		}
	})
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_extension WHERE extname = 'postgis_raster'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		if _, err := conn.Exec(ctx, `CREATE EXTENSION postgis_raster`); err != nil {
			t.Skipf("extension postgis_raster unavailable: %v", err)
		}
		created = true
	}
	if _, err := conn.Exec(ctx, drop+`CREATE SCHEMA `+nestedSchema+`; CREATE SCHEMA `+nestedPriv+`;`+nestedDDL); err != nil {
		t.Fatal(err)
	}
	return dbURL
}

func nestedMutate(c *config.Config) {
	c.Schema.Schemas = []string{nestedSchema}
	c.Filters.IndexedOnly = false
	c.Aggregates.Enabled = true
	c.Contracts = []confx.Contract{{Name: "internal"}, {Name: "public"}}
}

// leaked reports the first secret marker body contains: a value of a
// hidden table, of an @omit read column, or raster bytes (every raster in
// nestedDDL is a 2x2 grid whose WKB hex starts with the same header).
func leaked(body []byte) string {
	for _, m := range []string{"s3cr3t", "alice", "LOOT-", "topsecret", "hunter2", "pw", "0100000100", "secret_rast", "hidden_meta"} {
		if strings.Contains(string(body), m) {
			return m
		}
	}
	return ""
}

func TestNestedHiddenRowsNotExposed(t *testing.T) {
	nestedSetup(t)
	srv := testServer(t, nestedMutate)
	internal, public := srv.URL+"/api/internal", srv.URL+"/api/public"

	for _, base := range []string{internal, public} {
		for _, path := range []string{
			"/rpc/get_wrap", "/rpc/get_wraps", "/rpc/get_deep", "/rpc/get_secret_dom", "/rpc/get_secret_doms",
			"/rpc/get_fwrap", "/rpc/secret_tab", "/rpc/get_rast",
		} {
			if res, body := get(t, base+path); res.StatusCode != 404 || leaked(body) != "" {
				t.Errorf("GET %s = %d %s, want 404", base+path, res.StatusCode, body)
			}
		}
		if res, body := send(t, "POST", base+"/rpc/wrap_label", `{"w":{"label":"l"}}`, nil); res.StatusCode != 404 {
			t.Errorf("POST %s/rpc/wrap_label = %d %s, want 404", base, res.StatusCode, body)
		}
		res, body := get(t, base+"/holder?order=id")
		if res.StatusCode != 200 || leaked(body) != "" || !sameJSON(body, holderRows(base == internal)) {
			t.Errorf("GET %s/holder = %d %s", base, res.StatusCode, body)
		}
		for _, sel := range []string{"id,w", "id,sd", "id,f", "id,w->s", "id,w::text"} {
			if res, body := get(t, base+"/holder?select="+sel); res.StatusCode != 400 || leaked(body) != "" {
				t.Errorf("GET %s/holder?select=%s = %d %s, want 400", base, sel, res.StatusCode, body)
			}
		}
		if res, body := get(t, base+"/docs?select=id,wrap&limit=1"); res.StatusCode != 400 || leaked(body) != "" {
			t.Errorf("GET %s/docs?select=id,wrap = %d %s, want 400", base, res.StatusCode, body)
		}
		_, doc := get(t, base+"/openapi.json")
		for _, word := range []string{`"secrets"`, "/secrets", "stash", "secret_dom", "get_wrap", "fwrap", `"wrap"`, "get_rast"} {
			if strings.Contains(string(doc), word) {
				t.Errorf("%s/openapi.json mentions %s", base, word)
			}
		}
	}

	if res, body := get(t, public+"/rpc/get_cwrap"); res.StatusCode != 404 || strings.Contains(string(body), "ctoken") {
		t.Errorf("public get_cwrap = %d %s, want 404", res.StatusCode, body)
	}
	if res, body := get(t, internal+"/rpc/get_cwrap"); res.StatusCode != 200 || !sameJSON(body, `{"label":"c","c":{"id":1,"token":"ctoken-1"}}`) {
		t.Errorf("internal get_cwrap = %d %s", res.StatusCode, body)
	}
}

// holderRows is GET /holder: only id survives on public; internal keeps
// cw, whose csecrets row is visible there.
func holderRows(internal bool) string {
	if !internal {
		return `[{"id":1},{"id":2}]`
	}
	cw := `{"label":"c","c":{"id":1,"token":"ctoken-1"}}`
	return `[{"id":1,"cw":` + cw + `},{"id":2,"cw":` + cw + `}]`
}

func TestNestedRowsRenderReadableColumns(t *testing.T) {
	nestedSetup(t)
	srv := testServer(t, nestedMutate)
	internal, public := srv.URL+"/api/internal", srv.URL+"/api/public"
	doc := func(id int, titled bool) string {
		if !titled {
			return `{"id":` + strconv.Itoa(id) + `}`
		}
		return `{"id":` + strconv.Itoa(id) + `,"title":"doc ` + strconv.Itoa(id) + `"}`
	}

	for _, base := range []string{internal, public} {
		titled := base == internal
		d1, d2 := doc(1, titled), doc(2, titled)
		box := `{"label":"w","d":` + d1 + `,"ds":[` + d1 + `]}`
		for path, want := range map[string]string{
			"/rpc/one_doc":                            d1,
			"/rpc/docs_arr":                           `[` + d1 + `,` + d2 + `]`,
			"/rpc/docs_tab":                           `[{"n":1,"d":` + d1 + `}]`,
			"/rpc/docs_dwrap":                         box,
			"/rpc/docs_dwraps?limit=1":                `[` + box + `]`,
			"/rpc/doc_dom":                            d1,
			"/rpc/doc_doms?limit=1":                   `[` + d1 + `]`,
			"/docs?select=id,self&id=eq.1":            `[{"id":1,"self":` + d1 + `}]`,
			"/docs?select=id,selfarr&id=eq.1":         `[{"id":1,"selfarr":[` + d1 + `]}]`,
			"/docs?select=id,boxes&id=eq.1":           `[{"id":1,"boxes":[{"label":"b","d":` + d1 + `,"ds":[` + d1 + `]}]}]`,
			"/dholder?select=id,dw,darr,d,dd&id=eq.1": `[{"id":1,"dw":{"label":"h","d":` + d1 + `,"ds":[` + d1 + `]},"darr":[` + d1 + `],"d":` + d1 + `,"dd":` + d1 + `}]`,
			"/docs?select=id,dholder(id,d)&id=eq.1":   `[{"id":1,"dholder":[{"id":1,"d":` + d1 + `}]}]`,
			"/dholder?select=id,x:darr->0,y:dw->d->>secret,z:darr->0->>secret&id=eq.1": `[{"id":1,"x":` + d1 + `,"y":null,"z":null}]`,
		} {
			res, body := get(t, base+path)
			if res.StatusCode != 200 || leaked(body) != "" || !sameJSON(body, want) {
				t.Errorf("GET %s = %d %s, want %s", base+path, res.StatusCode, body, want)
			}
		}
		for _, path := range []string{
			"/dholder?select=id,dw::text&id=eq.1",
			"/dholder?select=dw::text.max()",
			"/dholder?id=eq.1",
		} {
			if res, body := get(t, base+path); res.StatusCode != 200 || leaked(body) != "" || titled != strings.Contains(string(body), "doc ") {
				t.Errorf("GET %s = %d %s", base+path, res.StatusCode, body)
			}
		}
		for _, path := range []string{
			"/dholder?darr=is.null", "/dholder?order=d", "/dholder?select=d.max()", "/dholder?select=dw,count()",
			"/dholder?distinct=darr", "/rpc/docs_tab?d=is.null", "/dholder?dw->d->>secret=eq.x",
		} {
			if res, body := get(t, base+path); res.StatusCode != 400 || leaked(body) != "" {
				t.Errorf("GET %s = %d %s, want 400", base+path, res.StatusCode, body)
			}
		}
		res, body := do(t, "GET", base+"/rpc/docs_dwraps?limit=1", map[string]string{"Accept": "text/csv"})
		if res.StatusCode != 200 || leaked(body) != "" || !strings.HasPrefix(string(body), "label,d,ds\r\n") {
			t.Errorf("CSV docs_dwraps = %d %q", res.StatusCode, body)
		}
		res, body = do(t, "GET", base+"/dholder?select=id,geom,dw&id=eq.1", map[string]string{"Accept": "application/geo+json"})
		if res.StatusCode != 200 || leaked(body) != "" || titled != strings.Contains(string(body), "doc 1") {
			t.Errorf("GeoJSON dholder = %d %s", res.StatusCode, body)
		}
		res, body = send(t, "PATCH", base+"/dholder/1?select=id,dw,darr,d", `{"note":"m"}`, map[string]string{"Prefer": "return=representation"})
		if res.StatusCode != 200 || leaked(body) != "" || titled != strings.Contains(string(body), "doc 1") {
			t.Errorf("PATCH dholder = %d %s", res.StatusCode, body)
		}

		_, raw := get(t, base+"/openapi.json")
		var oa struct {
			Components struct {
				Schemas map[string]struct {
					Properties map[string]json.RawMessage `json:"properties"`
				} `json:"schemas"`
			} `json:"components"`
		}
		if err := json.Unmarshal(raw, &oa); err != nil {
			t.Fatal(err)
		}
		dwrap := oa.Components.Schemas["dwrap"].Properties
		if _, ok := dwrap["r"]; ok || !strings.Contains(string(dwrap["d"]), `#/components/schemas/docs"`) || !strings.Contains(string(dwrap["ds"]), `#/components/schemas/docs"`) {
			t.Errorf("%s OpenAPI dwrap = %v", base, dwrap)
		}
		if d := oa.Components.Schemas["rpc.docs_tab"].Properties; d["r"] != nil || !strings.Contains(string(d["d"]), `#/components/schemas/docs"`) {
			t.Errorf("%s OpenAPI docs_tab = %v", base, d)
		}
	}
}
