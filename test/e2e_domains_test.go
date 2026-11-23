package e2e

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbr/internal/config"
)

const domainsDDL = `
CREATE SCHEMA pdbr_domains;
SET search_path TO pdbr_domains, public;
CREATE DOMAIN email AS text CHECK (VALUE ~ '^[^@]+@[^@]+$');
COMMENT ON DOMAIN email IS 'An email address.';
CREATE DOMAIN positive_int AS int CHECK (VALUE > 0);
CREATE DOMAIN small_positive AS positive_int CHECK (VALUE <= 100);
CREATE DOMAIN big_count AS bigint;
CREATE DOMAIN int_list AS int[];
CREATE DOMAIN spot AS public.geometry(Point,4326);
CREATE TABLE people (
  id int PRIMARY KEY,
  email email NOT NULL,
  aliases email[],
  score small_positive,
  visits big_count,
  lucky int_list,
  home spot
);
INSERT INTO people VALUES
  (1, 'ada@example.com', '{ada@x.org,countess@x.org}', 42, 12345678901234, '{3,7}', 'SRID=4326;POINT(1 2)'),
  (2, 'alan@example.com', '{}', 7, 1, '{}', NULL);
CREATE FUNCTION double_score(s small_positive) RETURNS positive_int
  LANGUAGE sql STABLE AS $$ SELECT (s * 2)::pdbr_domains.positive_int $$;
CREATE FUNCTION first_alias(a email[]) RETURNS email
  LANGUAGE sql STABLE AS $$ SELECT a[1] $$;
CREATE FUNCTION sum_list(l int_list) RETURNS int
  LANGUAGE sql STABLE AS $$ SELECT sum(x)::int FROM unnest(l) AS x $$;
CREATE FUNCTION spot_x(s spot) RETURNS float8
  LANGUAGE sql STABLE AS $$ SELECT public.ST_X(s) $$;
RESET search_path;
`

// TestDomains covers server.openapi_domains (on by default): a named
// component schema per domain over a built-in type with pattern and bounds
// from simple CHECKs, $refs from columns, arrays, bodies, RPC arguments and
// results; and that domain columns and arguments filter, write and call
// like their base types (lists of domain values included).
func TestDomains(t *testing.T) {
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
	t.Cleanup(func() {
		c, err := pgx.Connect(ctx, dbURL)
		if err != nil {
			t.Errorf("cleanup: %v", err)
			return
		}
		defer c.Close(ctx)
		_, _ = c.Exec(ctx, `DROP SCHEMA IF EXISTS pdbr_domains CASCADE`)
	})
	if _, err := conn.Exec(ctx, domainsDDL); err != nil {
		t.Fatal(err)
	}
	srv := testServer(t, func(cfg *config.Config) {
		cfg.Schema.Schemas = []string{"pdbr_domains"}
		cfg.Filters.IndexedOnly = false
	}).URL

	body, doc := fetchOpenAPI(t, srv)
	validateOpenAPI(t, oasSchema(t), body)
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	email, _ := schemas["email"].(map[string]any)
	if email["type"] != "string" || email["pattern"] != "^[^@]+@[^@]+$" || email["description"] == nil {
		t.Errorf("email schema = %v", email)
	}
	score, _ := schemas["small_positive"].(map[string]any)
	if score["type"] != "integer" || score["maximum"] != 100.0 || score["exclusiveMinimum"] != 0.0 {
		t.Errorf("small_positive schema = %v", score)
	}
	if lucky, _ := schemas["int_list"].(map[string]any); lucky["type"] != "array" {
		t.Errorf("int_list schema = %v", lucky)
	}
	if schemas["spot"] != nil {
		t.Error("a domain over geometry must keep GeoJSON")
	}
	people := schemas["people"].(map[string]any)["properties"].(map[string]any)
	if got := people["email"].(map[string]any)["$ref"]; got != "#/components/schemas/email" {
		t.Errorf("people.email = %v", people["email"])
	}
	if items := people["aliases"].(map[string]any)["items"].(map[string]any); items["$ref"] != "#/components/schemas/email" {
		t.Errorf("people.aliases = %v", people["aliases"])
	}

	reads := map[string]string{
		"/api/people?select=id,email,aliases,score,visits,lucky,home&id=eq.1": `[{"id":1,"email":"ada@example.com","aliases":["ada@x.org","countess@x.org"],"score":42,"visits":"12345678901234","lucky":[3,7],"home":{"type":"Point","coordinates":[1,2]}}]`,
		"/api/people?select=id&email=like.a*@example.com&score=gt.10":         `[{"id":1}]`,
		"/api/people?select=id&email=in.(alan@example.com,x@y.z)":             `[{"id":2}]`,
		"/api/people?select=id&aliases=cs.{ada@x.org}":                        `[{"id":1}]`,
		"/api/people?select=id&lucky=cs.{7}":                                  `[{"id":1}]`,
		"/api/people?select=id&score=in.(7,8)":                                `[{"id":2}]`,
		"/api/rpc/double_score?s=21":                                          `42`,
		"/api/rpc/first_alias?a={x@y.z,q@r.s}":                                `"x@y.z"`,
		"/api/rpc/sum_list?l={1,2,3}":                                         `6`,
	}
	for path, want := range reads {
		res, body := get(t, srv+path)
		if res.StatusCode != 200 || !sameJSON(body, want) {
			t.Errorf("GET %s = %d %s, want %s", path, res.StatusCode, body, want)
		}
	}
	calls := map[string][2]string{
		"double_score": {`{"s": 21}`, `42`},
		"first_alias":  {`{"a": ["x@y.z", "q@r.s"]}`, `"x@y.z"`},
		"sum_list":     {`{"l": [1, 2, 3]}`, `6`},
		"spot_x":       {`{"s": {"type": "Point", "coordinates": [5, 6]}}`, `5`},
	}
	for fn, c := range calls {
		res, body := send(t, "POST", srv+"/api/rpc/"+fn, c[0], nil)
		if res.StatusCode != 200 || !sameJSON(body, c[1]) {
			t.Errorf("POST rpc/%s = %d %s, want %s", fn, res.StatusCode, body, c[1])
		}
	}

	var created row
	sendJSON(t, "POST", srv+"/api/people?select=email,aliases,score,visits,lucky",
		`{"id": 3, "email": "cy@example.com", "aliases": ["c@x.org"], "score": 5, "visits": "9", "lucky": [1]}`, 201, &created)
	if got, _ := json.Marshal(created); !sameJSON(got, `{"email":"cy@example.com","aliases":["c@x.org"],"score":5,"visits":"9","lucky":[1]}`) {
		t.Errorf("created = %v", created)
	}
	res, body := send(t, "PATCH", srv+"/api/people?id=eq.3", `{"score": 500}`, nil)
	wantProblem(t, res, body, 400, "pg:23514")
}

// TestDomainsOff checks that server.openapi_domains: false inlines the
// base type's schema.
func TestDomainsOff(t *testing.T) {
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
	t.Cleanup(func() {
		c, err := pgx.Connect(ctx, dbURL)
		if err != nil {
			return
		}
		defer c.Close(ctx)
		_, _ = c.Exec(ctx, `DROP SCHEMA IF EXISTS pdbr_domains CASCADE`)
	})
	if _, err := conn.Exec(ctx, domainsDDL); err != nil {
		t.Fatal(err)
	}
	srv := testServer(t, func(cfg *config.Config) {
		cfg.Schema.Schemas = []string{"pdbr_domains"}
		cfg.Server.OpenAPIDomains = false
	}).URL
	_, doc := fetchOpenAPI(t, srv)
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	if schemas["email"] != nil {
		t.Error("domain component emitted with openapi_domains off")
	}
	people := schemas["people"].(map[string]any)["properties"].(map[string]any)
	if got := people["email"].(map[string]any)["type"]; got != "string" {
		t.Errorf("people.email = %v", people["email"])
	}
}
