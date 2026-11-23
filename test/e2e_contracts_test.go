package e2e

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/suprbdev/pdbcore/confx"
	"github.com/suprbdev/pdbr/internal/config"
)

const contractsSchema = "contracts_e2e"

// setupContracts creates the contracts_e2e schema: customers (email hidden
// from public), invoices (internal and partner only), audits (internal
// only) and two functions, one tagged into the default-exclude partner
// contract. It returns the database URL.
func setupContracts(t *testing.T) string {
	t.Helper()
	url := os.Getenv("PDBR_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	t.Cleanup(func() {
		execDDL(t, url, `DROP SCHEMA IF EXISTS `+contractsSchema+` CASCADE`,
			`DROP EVENT TRIGGER IF EXISTS pdbr_watch`, `DROP EVENT TRIGGER IF EXISTS pdbr_watch_drop`,
			`DROP FUNCTION IF EXISTS pdbr_watch_notify()`)
	})
	s := contractsSchema
	execDDL(t, url,
		`DROP SCHEMA IF EXISTS `+s+` CASCADE`,
		`CREATE SCHEMA `+s,
		`CREATE TABLE `+s+`.customers (id int GENERATED ALWAYS AS IDENTITY PRIMARY KEY, name text NOT NULL,
			email text UNIQUE, tier text)`,
		`COMMENT ON TABLE `+s+`.customers IS '@behavior +contract:partner'`,
		`COMMENT ON COLUMN `+s+`.customers.email IS E'@behavior -contract:public\nContact address.'`,
		`CREATE TABLE `+s+`.invoices (id int GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			customer_id int NOT NULL REFERENCES `+s+`.customers (id), amount int NOT NULL)`,
		`COMMENT ON TABLE `+s+`.invoices IS '@contract internal, partner'`,
		`CREATE TABLE `+s+`.audits (id int GENERATED ALWAYS AS IDENTITY PRIMARY KEY, note text)`,
		`COMMENT ON TABLE `+s+`.audits IS '@contract internal'`,
		`INSERT INTO `+s+`.customers (name, email, tier) VALUES ('acme', 'ops@acme.test', 'gold'), ('globex', 'it@globex.test', 'silver')`,
		`INSERT INTO `+s+`.invoices (customer_id, amount) VALUES (1, 100), (1, 250), (2, 75)`,
		`INSERT INTO `+s+`.audits (note) VALUES ('created')`,
		`CREATE FUNCTION `+s+`.gold_customers() RETURNS SETOF `+s+`.customers LANGUAGE sql STABLE AS $$ SELECT * FROM `+s+`.customers WHERE tier = 'gold' $$`,
		`COMMENT ON FUNCTION `+s+`.gold_customers() IS '@behavior +contract:partner'`,
		`CREATE FUNCTION `+s+`.audit_count() RETURNS bigint LANGUAGE sql STABLE AS $$ SELECT count(*) FROM `+s+`.audits $$`,
	)
	return url
}

func contractsMutate(c *config.Config) {
	c.Schema.Schemas = []string{contractsSchema}
	c.Filters.IndexedOnly = false
	c.Metrics.Enabled = true
	c.Contracts = []confx.Contract{
		{Name: "internal"},
		{Name: "public"},
		{Name: "partner", Default: "exclude", Path: "/partners"},
	}
}

type contractDoc struct {
	Servers []struct {
		URL string `json:"url"`
	} `json:"servers"`
	Paths      map[string]any `json:"paths"`
	Components struct {
		Schemas map[string]struct {
			Properties map[string]any `json:"properties"`
		} `json:"schemas"`
	} `json:"components"`
}

func TestContracts(t *testing.T) {
	setupContracts(t)
	srv := testServer(t, contractsMutate)
	internal, public, partner := srv.URL+"/api/internal", srv.URL+"/api/public", srv.URL+"/partners"

	t.Run("openapi per contract", func(t *testing.T) {
		var doc contractDoc
		getJSON(t, internal+"/openapi.json", &doc)
		if doc.Servers[0].URL != "/api/internal" || doc.Paths["/audits"] == nil || doc.Paths["/invoices"] == nil || doc.Paths["/rpc/audit_count"] == nil {
			t.Errorf("internal document: servers %+v, paths %v", doc.Servers, keys(doc.Paths))
		}
		if doc.Components.Schemas["customers"].Properties["email"] == nil {
			t.Error("internal customers schema lacks email")
		}

		res, body := get(t, public+"/openapi.json")
		if res.StatusCode != 200 || strings.Contains(strings.ToLower(string(body)), "email") {
			t.Errorf("public document %d mentions the hidden email column", res.StatusCode)
		}
		getJSON(t, public+"/openapi.json", &doc)
		if doc.Paths["/customers"] == nil || doc.Paths["/invoices"] != nil || doc.Paths["/audits"] != nil {
			t.Errorf("public paths = %v, want customers without invoices or audits", keys(doc.Paths))
		}

		getJSON(t, partner+"/openapi.json", &doc)
		if doc.Servers[0].URL != "/partners" || doc.Paths["/customers"] == nil || doc.Paths["/invoices"] == nil ||
			doc.Paths["/rpc/gold_customers"] == nil || doc.Paths["/audits"] != nil || doc.Paths["/rpc/audit_count"] != nil {
			t.Errorf("partner document: servers %+v, paths %v", doc.Servers, keys(doc.Paths))
		}
	})

	t.Run("hidden column absent from every surface", func(t *testing.T) {
		for _, path := range []string{
			"/customers?select=email",
			"/customers?select=*,email",
			"/customers?email=eq.ops@acme.test",
			"/customers?or=(email.eq.ops@acme.test,id.eq.0)",
			"/customers?order=email",
			"/customers/by/email/ops@acme.test",
			"/invoices?select=id",
			"/audits?select=id",
		} {
			if res, body := get(t, public+path); res.StatusCode != 400 && res.StatusCode != 404 {
				t.Errorf("public GET %s = %d %s, want 400 or 404", path, res.StatusCode, body)
			}
		}
		var rows []row
		getJSON(t, public+"/customers?select=*&order=id", &rows)
		if len(rows) != 2 || rows[0]["email"] != nil || rows[0]["name"] != "acme" {
			t.Errorf("public customers = %v", rows)
		}
		res, body := send(t, "POST", public+"/customers", `{"name":"x","email":"x@x.test"}`, nil)
		wantProblem(t, res, body, 400, "invalid_body")
		res, body = send(t, "PATCH", public+"/customers/1", `{"email":"x@x.test"}`, nil)
		wantProblem(t, res, body, 400, "invalid_body")
	})

	t.Run("the same column served elsewhere", func(t *testing.T) {
		var rows []row
		res, body := get(t, internal+"/customers?select=name,email&email=eq.ops@acme.test")
		if res.StatusCode != 200 || !sameJSON(body, `[{"name":"acme","email":"ops@acme.test"}]`) {
			t.Errorf("internal customers = %d %s", res.StatusCode, body)
		}
		getJSON(t, partner+"/invoices?select=amount,customers(email)&order=id", &rows)
		if len(rows) != 3 {
			t.Errorf("partner invoices = %v", rows)
		}
		res, body = get(t, partner+"/rpc/gold_customers?select=name")
		if res.StatusCode != 200 || !sameJSON(body, `[{"name":"acme"}]`) {
			t.Errorf("partner gold_customers = %d %s", res.StatusCode, body)
		}
		if res, _ := get(t, partner+"/audits"); res.StatusCode != 404 {
			t.Errorf("partner reached audits: %d", res.StatusCode)
		}
	})

	t.Run("routes", func(t *testing.T) {
		for path, want := range map[string]int{"/api/customers": 404, "/api/openapi.json": 404, "/healthz": 200, "/readyz": 200} {
			if res, _ := get(t, srv.URL+path); res.StatusCode != want {
				t.Errorf("GET %s = %d, want %d", path, res.StatusCode, want)
			}
		}
	})

	t.Run("metrics labelled by contract", func(t *testing.T) {
		out := scrapeMetrics(t, srv.URL+"/metrics")
		for _, series := range []string{
			`pdbr_http_requests_total{contract="internal",method="GET",route="resource",status_class="2xx"}`,
			`pdbr_http_requests_total{contract="public",method="GET",route="resource",status_class="2xx"}`,
			`pdbr_http_requests_total{contract="partner",method="GET",route="rpc",status_class="2xx"}`,
			`pdbr_db_pool_max_conns`,
		} {
			if got := metricValue(out, series); got < 1 {
				t.Errorf("%s = %v, want >= 1", series, got)
			}
		}
	})
}

func TestContractsWatchRebuildsEveryContract(t *testing.T) {
	url := setupContracts(t)
	srv := watchServer(t, url, nil, func(c *config.Config) {
		contractsMutate(c)
		c.Watch.PollInterval = 200 * time.Millisecond
	})
	execDDL(t, url,
		`COMMENT ON COLUMN `+contractsSchema+`.customers.tier IS '@behavior -contract:partner'`,
		`COMMENT ON TABLE `+contractsSchema+`.audits IS '@contract internal, public'`,
	)
	waitFor(t, "every contract rebuilt", 30*time.Second, func() bool {
		var part, pub contractDoc
		getJSON(t, srv.URL+"/partners/openapi.json", &part)
		getJSON(t, srv.URL+"/api/public/openapi.json", &pub)
		return part.Components.Schemas["customers"].Properties["tier"] == nil && pub.Paths["/audits"] != nil
	})
	var doc contractDoc
	getJSON(t, srv.URL+"/api/internal/openapi.json", &doc)
	if doc.Components.Schemas["customers"].Properties["tier"] == nil {
		t.Error("the internal contract lost the tier column")
	}
	out := scrapeMetrics(t, srv.URL+"/metrics")
	for _, name := range []string{"internal", "public", "partner"} {
		if got := metricValue(out, `pdbr_schema_rebuilds_total{contract="`+name+`",result="success"}`); got < 1 {
			t.Errorf("%s rebuilds = %v, want >= 1", name, got)
		}
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestContractsKeepRLS serves the fixture through a contract with RLS on:
// the contract narrows the API, the policies still decide the rows.
func TestContractsKeepRLS(t *testing.T) {
	srv := testServer(t, func(c *config.Config) {
		headersAuth(c)
		c.Contracts = []confx.Contract{{Name: "public"}}
	})
	count := func(hdr map[string]string) int {
		t.Helper()
		res, body := do(t, "GET", srv.URL+"/api/public/posts?select=id&limit=100", hdr)
		if res.StatusCode != 200 {
			t.Fatalf("posts = %d %s", res.StatusCode, body)
		}
		var rows []row
		if err := json.Unmarshal(body, &rows); err != nil {
			t.Fatal(err)
		}
		return len(rows)
	}
	if n := count(nil); n != 3 {
		t.Errorf("anonymous = %d through the contract, want 3", n)
	}
	if n := count(map[string]string{"X-Pdbr-Claim-Role": "app_user", "X-Pdbr-Claim-User-Id": "1"}); n != 4 {
		t.Errorf("app_user #1 = %d through the contract, want 4", n)
	}
}
