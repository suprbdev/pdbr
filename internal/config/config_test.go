package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/suprbdev/pdbcore/confx"
)

func valid() Config {
	cfg := DefaultConfig()
	cfg.RLS.Auth.JWTSecret = "test-secret"
	return cfg
}

func TestDefaults(t *testing.T) {
	cfg := valid()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
	want := map[string]any{
		"server.addr":                       ":8080",
		"server.base_path":                  "/api",
		"server.openapi":                    true,
		"server.request_timeout":            30 * time.Second,
		"server.readyz_timeout":             2 * time.Second,
		"server.max_body_bytes":             int64(1 << 20),
		"server.max_embed_depth":            5,
		"server.max_cost":                   100000,
		"server.max_page_size":              100,
		"server.etags":                      true,
		"server.csv":                        true,
		"server.compression":                false,
		"server.read_only":                  false,
		"server.allow_unfiltered_mutations": false,
		"server.aggregates":                 false,
		"server.tx_end":                     "commit",
		"rls.claims_prefix":                 "pdbq.claims",
		"rls.auth.header_prefix":            "X-Pdbr-Claim-",
		"rls.anonymous_role":                "anonymous",
		"watch.channel":                     "pdbr_ddl",
		"database.max_conns":                int32(10),
		"transactions.isolation":            "read_committed",
		"transactions.max_retries":          0,
		"errors.detail":                     "prod",
		"metrics.enabled":                   false,
		"metrics.path":                      "/metrics",
		"metrics.addr":                      "",
	}
	got := map[string]any{
		"server.addr":                       cfg.Server.Addr,
		"server.base_path":                  cfg.Server.BasePath,
		"server.openapi":                    cfg.Server.OpenAPI,
		"server.request_timeout":            cfg.Server.RequestTimeout,
		"server.readyz_timeout":             cfg.Server.ReadyzTimeout,
		"server.max_body_bytes":             cfg.Server.MaxBodyBytes,
		"server.max_embed_depth":            cfg.Server.MaxEmbedDepth,
		"server.max_cost":                   cfg.Server.MaxCost,
		"server.max_page_size":              cfg.Server.MaxPageSize,
		"server.etags":                      cfg.Server.Etags,
		"server.csv":                        cfg.Server.CSV,
		"server.compression":                cfg.Server.Compression,
		"server.read_only":                  cfg.Server.ReadOnly,
		"server.allow_unfiltered_mutations": cfg.Server.AllowUnfilteredMutations,
		"server.aggregates":                 cfg.Server.Aggregates,
		"server.tx_end":                     cfg.Server.TxEnd,
		"rls.claims_prefix":                 cfg.RLS.ClaimsPrefix,
		"rls.auth.header_prefix":            cfg.RLS.Auth.HeaderPrefix,
		"rls.anonymous_role":                cfg.RLS.AnonymousRole,
		"watch.channel":                     cfg.Watch.Channel,
		"database.max_conns":                cfg.Database.MaxConns,
		"transactions.isolation":            cfg.TX.Isolation,
		"transactions.max_retries":          cfg.TX.MaxRetries,
		"errors.detail":                     cfg.Errors.Detail,
		"metrics.enabled":                   cfg.Metrics.Enabled,
		"metrics.path":                      cfg.Metrics.Path,
		"metrics.addr":                      cfg.Metrics.Addr,
	}
	for k, w := range want {
		if !reflect.DeepEqual(got[k], w) {
			t.Errorf("%s = %#v, want %#v", k, got[k], w)
		}
	}
	if !reflect.DeepEqual(cfg.Schema.Schemas, []string{"public"}) {
		t.Errorf("schema.schemas = %v", cfg.Schema.Schemas)
	}
}

func TestValidateRules(t *testing.T) {
	cases := map[string]struct {
		mutate func(*Config)
		want   string
	}{
		"1 errors.detail": {func(c *Config) { c.Errors.Detail = "verbose" }, "errors.detail"},
		"2 auth.mode":     {func(c *Config) { c.RLS.Auth.Mode = "oauth" }, "rls.auth.mode"},
		"3 trusted proxy": {func(c *Config) { c.RLS.Auth.TrustedProxies = []string{"10.0.0.0/8", "not-an-ip"} }, "trusted_proxies"},
		"4 jwt secret":    {func(c *Config) { c.RLS.Auth.JWTSecret = "" }, "jwt_secret or rls.auth.jwks_url"},
		"5 jwt_type needs secret": {func(c *Config) {
			c.RLS.Auth.JWTType = "public.jwt"
			c.RLS.Auth.JWTSecret = ""
			c.RLS.Auth.JWKSURL = "https://x"
		}, "jwt_type requires"},
		"6 jwt_type qualified":   {func(c *Config) { c.RLS.Auth.JWTType = "jwt" }, "schema-qualified"},
		"7 anon role":            {func(c *Config) { c.RLS.AnonymousRole = "" }, "anonymous_role"},
		"8 isolation":            {func(c *Config) { c.TX.Isolation = "chaos" }, "transactions.isolation"},
		"9 watch+cache":          {func(c *Config) { c.Watch.Enabled = true; c.Schema.CachePath = "x" }, "watch.enabled cannot"},
		"10 watch channel":       {func(c *Config) { c.Watch.Enabled = true; c.Watch.Channel = "bad$$chan" }, "watch.channel"},
		"11 no schemas":          {func(c *Config) { c.Schema.Schemas = nil }, "schema.schemas"},
		"12 max_retries":         {func(c *Config) { c.TX.MaxRetries = -1 }, "max_retries"},
		"13 max page size":       {func(c *Config) { c.Server.MaxPageSize = 0 }, "max_page_size"},
		"14 log level":           {func(c *Config) { c.Log.Level = "loud" }, "log.level"},
		"15a base_path no slash": {func(c *Config) { c.Server.BasePath = "api" }, "base_path"},
		"15b base_path trailing": {func(c *Config) { c.Server.BasePath = "/api/" }, "base_path"},
		"16b tx_end":             {func(c *Config) { c.Server.TxEnd = "maybe" }, "server.tx_end"},
		"16 embed depth":         {func(c *Config) { c.Server.MaxEmbedDepth = 0 }, "max_embed_depth"},
		"17a anon role none":     {func(c *Config) { c.RLS.AnonymousRole = "none" }, "rls.anonymous_role"},
		"17b default role NONE":  {func(c *Config) { c.RLS.DefaultRole = "NONE" }, "rls.default_role"},
		"17c allowed role none":  {func(c *Config) { c.RLS.AllowedRoles = []string{"web_user", "None"} }, "rls.allowed_roles"},
		"17d anon role public":   {func(c *Config) { c.RLS.AnonymousRole = "public" }, "rls.anonymous_role"},
		"17e default role pg_":   {func(c *Config) { c.RLS.DefaultRole = "pg_read_all_data" }, "rls.default_role"},
		"17f role not ident":     {func(c *Config) { c.RLS.AnonymousRole = "web anon" }, "rls.anonymous_role"},
		"18a claims prefix":      {func(c *Config) { c.RLS.ClaimsPrefix = "plpgsql.claims" }, "rls.claims_prefix"},
		"18b claims prefix pg_":  {func(c *Config) { c.RLS.ClaimsPrefix = "pg_x.claims" }, "rls.claims_prefix"},
		"18c claims prefix syn":  {func(c *Config) { c.RLS.ClaimsPrefix = "a..b" }, "rls.claims_prefix"},
		"19a metrics path":       {func(c *Config) { c.Metrics.Path = "metrics" }, "metrics.path"},
		"19b metrics path root":  {func(c *Config) { c.Metrics.Path = "/" }, "metrics.path"},
		"19c metrics path query": {func(c *Config) { c.Metrics.Path = "/metrics?x=1" }, "metrics.path"},
		"20a metrics addr":       {func(c *Config) { c.Metrics.Addr = "9090" }, "metrics.addr"},
		"20b metrics addr main":  {func(c *Config) { c.Metrics.Addr = ":8080" }, "metrics.addr"},
		"21a metrics healthz":    {func(c *Config) { c.Metrics.Enabled = true; c.Metrics.Path = "/healthz" }, "collides"},
		"21b metrics readyz":     {func(c *Config) { c.Metrics.Enabled = true; c.Metrics.Path = "/readyz" }, "collides"},
		"21c metrics base_path":  {func(c *Config) { c.Metrics.Enabled = true; c.Metrics.Path = "/api" }, "collides"},
		"21d metrics under base": {func(c *Config) { c.Metrics.Enabled = true; c.Metrics.Path = "/api/metrics" }, "collides"},
		"21e metrics openapi": {func(c *Config) {
			c.Metrics.Enabled = true
			c.Server.BasePath = ""
			c.Metrics.Path = "/openapi.json"
		}, "collides"},
		"22a contract name":    {func(c *Config) { c.Contracts = []confx.Contract{{Name: "Public"}} }, "contracts[0].name"},
		"22b contract overlap": {func(c *Config) { c.Contracts = []confx.Contract{{Name: "a"}, {Name: "b", Path: "/api/a/b"}} }, "overlaps"},
		"22c contract root":    {func(c *Config) { c.Contracts = []confx.Contract{{Name: "a", Path: "/"}, {Name: "b"}} }, "overlaps"},
		"22d contract smart-comments": {func(c *Config) {
			c.Contracts = []confx.Contract{{Name: "a"}}
			c.Plugins.Disabled = []string{"smart-comments"}
		}, "smart-comments"},
		"22e contract metrics": {func(c *Config) {
			c.Contracts = []confx.Contract{{Name: "a"}}
			c.Metrics.Enabled = true
			c.Metrics.Path = "/api/a/metrics"
		}, "collides"},
	}
	for name, tc := range cases {
		cfg := valid()
		tc.mutate(&cfg)
		err := cfg.Validate()
		if err == nil {
			t.Errorf("%s: expected validation error", name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not mention %q", name, err, tc.want)
		}
	}
	ok := map[string]func(*Config){
		"empty base_path": func(c *Config) { c.Server.BasePath = "" },
		"v1 base_path":    func(c *Config) { c.Server.BasePath = "/v1" },
		"strict":          func(c *Config) { c.Errors.Detail = "strict" },
		"rls off no jwt":  func(c *Config) { c.RLS.Enabled = false; c.RLS.Auth.JWTSecret = "" },
		"mode none":       func(c *Config) { c.RLS.Auth.Mode = "none"; c.RLS.Auth.JWTSecret = "" },
		"metrics on":      func(c *Config) { c.Metrics.Enabled = true },
		"metrics apiish":  func(c *Config) { c.Metrics.Enabled = true; c.Metrics.Path = "/apis/metrics" },
		"metrics own addr": func(c *Config) {
			c.Metrics.Enabled = true
			c.Metrics.Addr = "127.0.0.1:9090"
			c.Metrics.Path = "/api/metrics"
		},
		"metrics root api": func(c *Config) { c.Metrics.Enabled = true; c.Server.BasePath = "" },
		"metrics off path": func(c *Config) { c.Metrics.Path = "/healthz" },
		"contracts": func(c *Config) {
			c.Contracts = []confx.Contract{{Name: "public"}, {Name: "partner", Default: "exclude", Path: "/partners"}}
			c.Metrics.Enabled = true
			c.Metrics.Path = "/api/metrics"
		},
		"contract at root": func(c *Config) { c.Contracts = []confx.Contract{{Name: "internal", Path: "/"}} },
	}
	for name, mutate := range ok {
		cfg := valid()
		mutate(&cfg)
		if err := cfg.Validate(); err != nil {
			t.Errorf("%s: unexpected error: %v", name, err)
		}
	}
}

func TestValidateReportsAllViolations(t *testing.T) {
	cfg := valid()
	cfg.Errors.Detail = "x"
	cfg.Log.Level = "y"
	cfg.Server.MaxEmbedDepth = 0
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, s := range []string{"errors.detail", "log.level", "max_embed_depth"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error %q missing %q", err, s)
		}
	}
}

func TestLoadLayering(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pdbr.yaml")
	yaml := `
database:
  url: "postgres://file/db"
server:
  addr: ":9999"
  base_path: "/file"
rls:
  enabled: false
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PDBR_SERVER_ADDR", ":7777")
	t.Setenv("PDBR_SERVER_BASE__PATH", "/v1")
	t.Setenv("PDBR_FILTERS_INDEXED__ONLY", "false")
	t.Setenv("PDBR_SCHEMA_SCHEMAS", "public,audit")
	t.Setenv("PDBR_SCHEMA_EXPOSE__PARTITIONS", "true")

	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.String("server.addr", "", "")
	fs.Int("server.max_page_size", 0, "")
	if err := fs.Parse([]string{"--server.addr=:5555"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, confx.ChangedFlags(fs, "config"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.URL != "postgres://file/db" {
		t.Errorf("file value lost: %q", cfg.Database.URL)
	}
	if cfg.Server.Addr != ":5555" {
		t.Errorf("flag did not override env: %q", cfg.Server.Addr)
	}
	if cfg.Server.BasePath != "/v1" {
		t.Errorf("env did not override file: %q", cfg.Server.BasePath)
	}
	if cfg.Filters.IndexedOnly {
		t.Error("PDBR_FILTERS_INDEXED__ONLY not applied")
	}
	if !reflect.DeepEqual(cfg.Schema.Schemas, []string{"public", "audit"}) {
		t.Errorf("schemas = %v", cfg.Schema.Schemas)
	}
	if !cfg.Schema.ExposePartitions || !cfg.Schema.Functions {
		t.Errorf("schema section = %+v", cfg.Schema)
	}
	if cfg.Server.MaxPageSize != 100 {
		t.Errorf("unset flag clobbered default: %d", cfg.Server.MaxPageSize)
	}
	if cfg.Server.MaxEmbedDepth != 5 {
		t.Errorf("default lost: %d", cfg.Server.MaxEmbedDepth)
	}
}

func TestEnvKeyMapper(t *testing.T) {
	m := confx.EnvKeyMapper(EnvPrefix)
	cases := map[string]string{
		"PDBR_DATABASE_URL":           "database.url",
		"PDBR_SERVER_BASE__PATH":      "server.base_path",
		"PDBR_SERVER_MAX__PAGE__SIZE": "server.max_page_size",
		"PDBR_RLS_AUTH_JWT__SECRET":   "rls.auth.jwt_secret",
		"PDBR_FILTERS_INDEXED__ONLY":  "filters.indexed_only",
	}
	for in, want := range cases {
		if got := m(in); got != want {
			t.Errorf("EnvKeyMapper(%q) = %q, want %q", in, got, want)
		}
	}
}

// koanfPaths walks the Config struct and returns every leaf koanf path.
func koanfPaths(t reflect.Type, prefix string) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		key := f.Tag.Get("koanf")
		if key == "" || key == "-" {
			continue
		}
		if f.Anonymous && strings.HasPrefix(key, ",") {
			out = append(out, koanfPaths(f.Type, prefix)...)
			continue
		}
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		if f.Type.Kind() == reflect.Struct && f.Type != reflect.TypeOf(time.Duration(0)) {
			out = append(out, koanfPaths(f.Type, path)...)
			continue
		}
		out = append(out, path)
	}
	return out
}

func TestExampleYAMLCoversEverything(t *testing.T) {
	out := ExampleYAML()
	if !strings.HasPrefix(out, "# pdbr reference configuration.") {
		t.Errorf("unexpected header: %q", out[:40])
	}
	for _, path := range koanfPaths(reflect.TypeOf(Config{}), "") {
		parts := strings.Split(path, ".")
		leaf := parts[len(parts)-1]
		indent := strings.Repeat("  ", len(parts)-1)
		if !strings.Contains(out, "\n"+indent+leaf+":") {
			t.Errorf("example yaml missing %s", path)
		}
	}
	for _, s := range []string{
		"# Boot from this schema cache file instead of introspecting (see pdbr schema dump).",
		"X-Pdbr-Claim-",
		"/rpc/{fn}",
		"problem responses",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("pdbr wording missing: %q", s)
		}
	}
	for _, s := range []string{"pdbq schema dump", "X-Pdbq-Claim-", "GraphQL", "queries/mutations"} {
		if strings.Contains(out, s) {
			t.Errorf("pdbq wording leaked: %q", s)
		}
	}
	if !strings.Contains(out, "base_path: \"/api\"") {
		t.Error("defaults not rendered")
	}
}

func TestExampleYAMLMatchesCommittedFile(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("..", "..", "examples", "pdbr.example.yaml"))
	if err != nil {
		t.Skipf("examples/pdbr.example.yaml not present: %v", err)
	}
	if got := ExampleYAML(); string(want) != got {
		t.Error("examples/pdbr.example.yaml is stale: run `make example-config`")
	}
}

func TestContractBase(t *testing.T) {
	cfg := DefaultConfig()
	for _, tc := range []struct {
		ct   confx.Contract
		want string
	}{
		{confx.Contract{}, "/api"},
		{confx.Contract{Name: "public"}, "/api/public"},
		{confx.Contract{Name: "p", Path: "/partners"}, "/partners"},
		{confx.Contract{Name: "i", Path: "/"}, ""},
	} {
		if got := cfg.ContractBase(tc.ct); got != tc.want {
			t.Errorf("ContractBase(%+v) = %q, want %q", tc.ct, got, tc.want)
		}
	}
	cfg.Contracts = []confx.Contract{{Name: "public"}}
	if ct, err := cfg.Contract("public"); err != nil || ct.Name != "public" {
		t.Errorf("Contract(public) = %+v, %v", ct, err)
	}
	if _, err := cfg.Contract("nope"); err == nil {
		t.Error("unknown contract accepted")
	}
}
