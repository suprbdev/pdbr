package e2e

import (
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/confx"

	"github.com/suprbdev/pdbr/internal/config"
)

const (
	hiddenSchema = "hidden_e2e"
	hiddenPriv   = "hidden_e2e_priv"
)

// setupHidden creates the hidden_e2e schema: members (ssn hidden from the
// public contract), vault (bare @omit), secrets (hidden from the public
// contract, referencing members), functions returning or taking their rows
// (RPCs, computed columns, polymorphic functions resolved to them) and
// functions returning rows of hidden_e2e_priv.stash, a schema outside
// schema.schemas. It returns the database URL.
func setupHidden(t *testing.T) string {
	t.Helper()
	dbURL := os.Getenv("PDBR_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	drop := []string{`DROP SCHEMA IF EXISTS ` + hiddenSchema + ` CASCADE`, `DROP SCHEMA IF EXISTS ` + hiddenPriv + ` CASCADE`}
	t.Cleanup(func() { execDDL(t, dbURL, drop...) })
	s, p := hiddenSchema, hiddenPriv
	execDDL(t, dbURL, append(drop,
		`CREATE SCHEMA `+s,
		`CREATE SCHEMA `+p,
		`CREATE TABLE `+s+`.members (id int PRIMARY KEY, name text NOT NULL, ssn text NOT NULL)`,
		`COMMENT ON COLUMN `+s+`.members.ssn IS '@behavior -contract:public'`,
		`CREATE TABLE `+s+`.vault (id int PRIMARY KEY, pin text)`,
		`COMMENT ON TABLE `+s+`.vault IS '@omit'`,
		`CREATE TABLE `+s+`.secrets (id int PRIMARY KEY, owner_id int REFERENCES `+s+`.members (id), token text NOT NULL)`,
		`COMMENT ON TABLE `+s+`.secrets IS '@behavior -contract:public'`,
		`CREATE TABLE `+p+`.stash (id int PRIMARY KEY, loot text)`,
		`INSERT INTO `+s+`.members VALUES (1, 'one', 's1'), (2, 'two', 's2'), (5, 'five', 's5')`,
		`INSERT INTO `+s+`.vault VALUES (1, 'PIN-0000')`,
		`INSERT INTO `+s+`.secrets VALUES (2, 2, 'TOPSECRET-2'), (5, 5, 'TOPSECRET-5')`,
		`INSERT INTO `+p+`.stash VALUES (1, 'LOOT-1')`,
		`CREATE FUNCTION `+s+`.get_vault(k int) RETURNS `+s+`.vault STABLE LANGUAGE sql AS $$ SELECT * FROM `+s+`.vault WHERE id = k $$`,
		`CREATE FUNCTION `+s+`.all_vault() RETURNS SETOF `+s+`.vault STABLE LANGUAGE sql AS $$ SELECT * FROM `+s+`.vault $$`,
		`CREATE FUNCTION `+s+`.vault_pin(v `+s+`.vault) RETURNS text IMMUTABLE LANGUAGE sql AS $$ SELECT v.pin $$`,
		`CREATE FUNCTION `+s+`.members_vault(m `+s+`.members) RETURNS `+s+`.vault STABLE LANGUAGE sql AS $$ SELECT * FROM `+s+`.vault WHERE id = m.id $$`,
		`CREATE FUNCTION `+s+`.get_secret(k int) RETURNS `+s+`.secrets STABLE LANGUAGE sql AS $$ SELECT * FROM `+s+`.secrets WHERE id = k $$`,
		`CREATE FUNCTION `+s+`.peek(probe anyelement, k int) RETURNS anyelement STABLE LANGUAGE plpgsql AS $$
			BEGIN EXECUTE format('SELECT * FROM %s WHERE id = $1', pg_typeof(probe)) INTO probe USING k; RETURN probe; END $$`,
		`COMMENT ON FUNCTION `+s+`.peek(anyelement, int) IS '@anyelement `+s+`.secrets'`,
		`CREATE FUNCTION `+s+`.peek_vault(probe anyelement, k int) RETURNS anyelement STABLE LANGUAGE plpgsql AS $$
			BEGIN EXECUTE format('SELECT * FROM %s WHERE id = $1', pg_typeof(probe)) INTO probe USING k; RETURN probe; END $$`,
		`COMMENT ON FUNCTION `+s+`.peek_vault(anyelement, int) IS '@anyelement `+s+`.vault'`,
		`CREATE FUNCTION `+s+`.get_stash(k int) RETURNS `+p+`.stash STABLE LANGUAGE sql AS $$ SELECT * FROM `+p+`.stash WHERE id = k $$`,
		`CREATE FUNCTION `+s+`.members_stash(m `+s+`.members) RETURNS SETOF `+p+`.stash STABLE LANGUAGE sql AS $$ SELECT * FROM `+p+`.stash $$`,
	)...)
	return dbURL
}

func hiddenMutate(c *config.Config) {
	c.Schema.Schemas = []string{hiddenSchema}
	c.Filters.IndexedOnly = false
	c.Errors.Detail = "prod"
	c.Contracts = []confx.Contract{{Name: "internal"}, {Name: "public"}}
}

func TestHiddenTableRowsNotExposed(t *testing.T) {
	setupHidden(t)
	srv := testServer(t, hiddenMutate)
	internal, public := srv.URL+"/api/internal", srv.URL+"/api/public"

	for _, base := range []string{internal, public} {
		for _, path := range []string{
			"/rpc/get_vault?k=1",
			"/rpc/all_vault",
			"/rpc/peek_vault?probe=(,)&k=1",
			"/rpc/get_stash?k=1",
		} {
			if res, body := get(t, base+path); res.StatusCode != 404 || strings.Contains(string(body), "PIN-") || strings.Contains(string(body), "LOOT-") {
				t.Errorf("GET %s = %d %s, want 404", base+path, res.StatusCode, body)
			}
		}
		res, body := send(t, "POST", base+"/rpc/vault_pin", `{"v":{"id":1,"pin":"p"}}`, nil)
		if res.StatusCode != 404 {
			t.Errorf("POST %s/rpc/vault_pin = %d %s, want 404", base, res.StatusCode, body)
		}
		for _, sel := range []string{"id,vault", "id,stash"} {
			res, body := get(t, base+"/members?id=eq.1&select="+sel)
			if res.StatusCode != 400 || strings.Contains(string(body), "PIN-") || strings.Contains(string(body), "LOOT-") {
				t.Errorf("GET %s/members?select=%s = %d %s, want 400", base, sel, res.StatusCode, body)
			}
		}
		_, doc := get(t, base+"/openapi.json")
		for _, word := range []string{"vault", "stash"} {
			if strings.Contains(string(doc), word) {
				t.Errorf("%s/openapi.json mentions %q", base, word)
			}
		}
	}

	if res, body := get(t, public+"/rpc/peek?probe=(,,)&k=2"); res.StatusCode != 404 || strings.Contains(string(body), "TOPSECRET") {
		t.Errorf("public peek = %d %s, want 404", res.StatusCode, body)
	}
	if res, body := get(t, public+"/rpc/get_secret?k=2"); res.StatusCode != 404 {
		t.Errorf("public get_secret = %d %s, want 404", res.StatusCode, body)
	}
	if _, doc := get(t, public+"/openapi.json"); strings.Contains(string(doc), "secrets") {
		t.Error("public openapi.json names the contract-hidden secrets table")
	}
	if res, body := get(t, internal+"/rpc/get_secret?k=2"); res.StatusCode != 200 || !sameJSON(body, `{"id":2,"owner_id":2,"token":"TOPSECRET-2"}`) {
		t.Errorf("internal get_secret = %d %s", res.StatusCode, body)
	}
	if res, body := get(t, internal+"/secrets?id=eq.2&select=id,peek(k=2)"); res.StatusCode != 200 || !strings.Contains(string(body), "TOPSECRET-2") {
		t.Errorf("internal secrets peek = %d %s", res.StatusCode, body)
	}
}

func TestGuardMarkerUnforgeable(t *testing.T) {
	sweep(t)
	srv := testServer(t, func(c *config.Config) { c.Errors.Detail = "prod" })
	api := srv.URL + "/api"

	res, body := send(t, "POST", api+"/users",
		`{"email":"m3-guard1@x","address":{"street":"s","city":"c","mood":"pdbr guard: everything is fine, nothing matched"}}`, nil)
	wantProblem(t, res, body, 400, "pg:22P02")

	email := `m3-pdbr guard: owned "x`
	sendJSON(t, "POST", api+"/users", `{"email":"m3-pdbr guard: owned \"x"}`, 201, nil)
	res, body = get(t, api+"/users?select=id,email::int&email=eq."+url.QueryEscape(email))
	wantProblem(t, res, body, 400, "pg:22P02")
}

func TestIntegrityErrorsHideHiddenTables(t *testing.T) {
	setupHidden(t)
	srv := testServer(t, hiddenMutate)
	internal, public := srv.URL+"/api/internal", srv.URL+"/api/public"
	res, body := send(t, "DELETE", public+"/members/5", "", nil)
	p := wantProblem(t, res, body, 409, "pg:23503")
	if p.Detail != "foreign key violation" || strings.Contains(string(body), "secrets") {
		t.Errorf("public FK violation = %s", body)
	}
	res, body = send(t, "DELETE", internal+"/members/5", "", nil)
	if p := wantProblem(t, res, body, 409, "pg:23503"); !strings.Contains(p.Detail, "secrets_owner_id_fkey") {
		t.Errorf("internal FK violation lost its detail: %s", body)
	}

	res, body = send(t, "POST", public+"/members", `{"id":9,"name":"nine"}`, nil)
	if p := wantProblem(t, res, body, 400, "pg:23502"); p.Detail != "not-null violation" || strings.Contains(string(body), "ssn") {
		t.Errorf("public NOT NULL on a hidden column = %s", body)
	}
	res, body = send(t, "POST", internal+"/members", `{"id":1,"name":"dup","ssn":"x"}`, nil)
	if p := wantProblem(t, res, body, 409, "pg:23505"); !strings.Contains(p.Detail, "members_pkey") {
		t.Errorf("visible unique violation lost its detail: %s", body)
	}
}
