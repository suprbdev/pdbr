package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/suprbdev/pdbcore/auth"
	"github.com/suprbdev/pdbcore/confx"
	"github.com/suprbdev/pdbcore/confx/cliflags"
)

func valid() Config {
	cfg := DefaultConfig()
	cfg.Auth.JWT.Secret = "test-secret"
	return cfg
}

func TestDefaults(t *testing.T) {
	cfg := valid()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
	want := map[string]any{
		"server.addr":                    ":8080",
		"server.base_path":               "",
		"server.request_timeout":         30 * time.Second,
		"server.readyz_timeout":          2 * time.Second,
		"server.etags":                   true,
		"server.csv":                     true,
		"server.compression":             false,
		"server.read_only":               false,
		"openapi.enabled":                true,
		"openapi.title":                  "pdbr",
		"openapi.server_url":             "",
		"limits.max_body_bytes":          int64(1 << 20),
		"limits.max_depth":               5,
		"limits.max_cost":                100000,
		"limits.max_page_size":           100,
		"limits.max_headline_document":   65536,
		"schema.domains":                 true,
		"mutations.allow_unfiltered":     false,
		"aggregates.enabled":             false,
		"transactions.end":               "commit",
		"transactions.partial.enabled":   false,
		"transactions.partial.max_items": 1000,
		"database.statement_timeout":     25 * time.Second,
		"auth.mode":                      auth.ModeJWT,
		"auth.jwt.jwks_cache_ttl":        time.Hour,
		"auth.jwt.role_claim_path":       "role",
		"auth.headers.prefix":            "X-Pdbr-Claim-",
		"auth.headers.role_claim":        "role",
		"roles.switch":                   true,
		"roles.anonymous":                "",
		"claims.prefix":                  "jwt.claims",
		"claims.json_setting":            "",
		"log.format":                     "text",
		"watch.channel":                  "pdbr_ddl",
		"database.max_conns":             int32(10),
		"transactions.isolation":         "read_committed",
		"transactions.max_retries":       0,
		"errors.detail":                  "prod",
		"metrics.enabled":                false,
		"metrics.path":                   "/metrics",
		"metrics.addr":                   "",
	}
	got := map[string]any{
		"server.addr":                    cfg.Server.Addr,
		"server.base_path":               cfg.Server.BasePath,
		"server.request_timeout":         cfg.Server.RequestTimeout,
		"server.readyz_timeout":          cfg.Server.ReadyzTimeout,
		"server.etags":                   cfg.Server.Etags,
		"server.csv":                     cfg.Server.CSV,
		"server.compression":             cfg.Server.Compression,
		"server.read_only":               cfg.Server.ReadOnly,
		"openapi.enabled":                cfg.OpenAPI.Enabled,
		"openapi.title":                  cfg.OpenAPI.Title,
		"openapi.server_url":             cfg.OpenAPI.ServerURL,
		"limits.max_body_bytes":          cfg.Limits.MaxBodyBytes,
		"limits.max_depth":               cfg.Limits.MaxDepth,
		"limits.max_cost":                cfg.Limits.MaxCost,
		"limits.max_page_size":           cfg.Limits.MaxPageSize,
		"limits.max_headline_document":   cfg.Limits.MaxHeadlineDocument,
		"schema.domains":                 cfg.Schema.Domains,
		"mutations.allow_unfiltered":     cfg.Mutations.AllowUnfiltered,
		"aggregates.enabled":             cfg.Aggregates.Enabled,
		"transactions.end":               cfg.TX.End,
		"transactions.partial.enabled":   cfg.TX.Partial.Enabled,
		"transactions.partial.max_items": cfg.TX.Partial.MaxItems,
		"database.statement_timeout":     cfg.Database.StatementTimeout,
		"auth.mode":                      cfg.Auth.Mode,
		"auth.jwt.jwks_cache_ttl":        cfg.Auth.JWT.JWKSCacheTTL,
		"auth.jwt.role_claim_path":       cfg.Auth.JWT.RoleClaimPath,
		"auth.headers.prefix":            cfg.Auth.Headers.Prefix,
		"auth.headers.role_claim":        cfg.Auth.Headers.RoleClaim,
		"roles.switch":                   cfg.Roles.Switch,
		"roles.anonymous":                cfg.Roles.Anonymous,
		"claims.prefix":                  cfg.Claims.Prefix,
		"claims.json_setting":            cfg.Claims.JSONSetting,
		"log.format":                     cfg.Log.Format,
		"watch.channel":                  cfg.Watch.Channel,
		"database.max_conns":             cfg.Database.MaxConns,
		"transactions.isolation":         cfg.TX.Isolation,
		"transactions.max_retries":       cfg.TX.MaxRetries,
		"errors.detail":                  cfg.Errors.Detail,
		"metrics.enabled":                cfg.Metrics.Enabled,
		"metrics.path":                   cfg.Metrics.Path,
		"metrics.addr":                   cfg.Metrics.Addr,
	}
	for k, w := range want {
		if !reflect.DeepEqual(got[k], w) {
			t.Errorf("%s = %#v, want %#v", k, got[k], w)
		}
	}
	if !reflect.DeepEqual(cfg.Schema.Schemas, []string{"public"}) {
		t.Errorf("schema.schemas = %v", cfg.Schema.Schemas)
	}
	if cfg.Database.StatementTimeout >= cfg.Server.RequestTimeout {
		t.Errorf("statement_timeout %s must stay below request_timeout %s", cfg.Database.StatementTimeout, cfg.Server.RequestTimeout)
	}
	if got := cfg.Plugins.Settings; got != DefaultPluginSettings() || !got.AdvancedFilters.Relations || got.NestedMutations.MaxDepth != 3 || !got.PostGIS.Aggregates {
		t.Errorf("plugins.settings = %+v", got)
	}
}

func TestDefaultsNeedCredentials(t *testing.T) {
	cfg := DefaultConfig()
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "auth.jwt.secret, auth.jwt.secret_file or auth.jwt.jwks_url is required") {
		t.Fatalf("defaults without a secret: %v", err)
	}
	cfg.Auth.Mode = auth.ModeNone
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "roles.anonymous is required in auth mode none") {
		t.Fatalf("mode none with role switching and no anonymous role: %v", err)
	}
	cfg.Roles.Switch = false
	if err := cfg.Validate(); err != nil {
		t.Fatalf("mode none as the connection role: %v", err)
	}
	cfg = DefaultConfig()
	cfg.Auth.Mode = auth.ModeNone
	cfg.Roles.Anonymous = "anonymous"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("mode none with an anonymous role: %v", err)
	}
}

func TestUseConnectionIdentity(t *testing.T) {
	cfg, err := LoadUnvalidated("", nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg.UseConnectionIdentity()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("CLI identity on defaults: %v", err)
	}
	if cfg.Auth.Mode != auth.ModeNone || cfg.Roles.Switch {
		t.Fatalf("auth/roles = %+v %+v", cfg.Auth, cfg.Roles)
	}
}

func TestStarterLoadsAndValidates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pdbr.yaml")
	if err := os.WriteFile(path, []byte(Starter), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, nil)
	if err != nil {
		t.Fatalf("starter: %v", err)
	}
	if cfg.Auth.Mode != auth.ModeNone || cfg.Roles.Switch || cfg.Database.URL == "" {
		t.Fatalf("starter auth/roles/db = %+v %+v %q", cfg.Auth, cfg.Roles, cfg.Database.URL)
	}
}

func TestValidateRules(t *testing.T) {
	cases := map[string]struct {
		mutate func(*Config)
		want   string
	}{
		"1 errors.detail": {func(c *Config) { c.Errors.Detail = "verbose" }, "errors.detail"},
		"2 auth.mode":     {func(c *Config) { c.Auth.Mode = "oauth" }, "auth.mode"},
		"3 trusted proxy": {func(c *Config) { c.Auth.Headers.TrustedProxies = []string{"10.0.0.0/8", "not-an-ip"} }, "trusted_proxies"},
		"4 jwt secret":    {func(c *Config) { c.Auth.JWT.Secret = "" }, "auth.jwt.secret, auth.jwt.secret_file or auth.jwt.jwks_url"},
		"5 mint_type needs secret": {func(c *Config) {
			c.Auth.MintType = "public.jwt"
			c.Auth.JWT.Secret = ""
			c.Auth.JWT.JWKSURL = "https://x"
		}, "mint_type requires"},
		"6 mint_type qualified":      {func(c *Config) { c.Auth.MintType = "jwt" }, "schema-qualified"},
		"7a headers without proxies": {func(c *Config) { c.Auth.Mode = auth.ModeHeaders }, "auth.headers.trusted_proxies is required"},
		"7b secret file missing":     {func(c *Config) { c.Auth.JWT.Secret = ""; c.Auth.JWT.SecretFile = "/nonexistent/secret" }, "auth.jwt.secret_file"},
		"7c secret and file":         {func(c *Config) { c.Auth.JWT.SecretFile = "/nonexistent/secret" }, "mutually exclusive"},
		"7d role claim path":         {func(c *Config) { c.Auth.JWT.RoleClaimPath = "a..b" }, "auth.jwt.role_claim_path"},
		"7e negative leeway":         {func(c *Config) { c.Auth.JWT.Leeway = -time.Second }, "auth.jwt.leeway"},
		"7f json setting":            {func(c *Config) { c.Claims.JSONSetting = "nodot" }, "claims.json_setting"},
		"7g request timeout zero":    {func(c *Config) { c.Server.RequestTimeout = 0 }, "server.request_timeout: 0s must be >= 1s"},
		"7h request timeout short":   {func(c *Config) { c.Server.RequestTimeout = 500 * time.Millisecond }, "server.request_timeout: 500ms must be >= 1s"},
		"7g json under prefix":       {func(c *Config) { c.Claims.JSONSetting = "jwt.claims.all" }, "lies under claims.prefix"},
		"7h unknown plugin":          {func(c *Config) { c.Plugins.Disabled = []string{"graphiql"} }, `unknown plugin "graphiql"`},
		"7i kebab plugin":            {func(c *Config) { c.Plugins.Disabled = []string{"nested-mutations"} }, `did you mean "nested_mutations"?`},
		"7j nested max_depth":        {func(c *Config) { c.Plugins.Settings.NestedMutations.MaxDepth = 0 }, "plugins.settings.nested_mutations.max_depth"},
		"7k log format":              {func(c *Config) { c.Log.Format = "logfmt" }, "log.format"},
		"8 isolation":                {func(c *Config) { c.TX.Isolation = "chaos" }, "transactions.isolation"},
		"9 watch+cache":              {func(c *Config) { c.Watch.Enabled = true; c.Schema.CachePath = "x" }, "watch.enabled cannot"},
		"10 watch channel":           {func(c *Config) { c.Watch.Enabled = true; c.Watch.Channel = "bad$$chan" }, "watch.channel"},
		"11 no schemas":              {func(c *Config) { c.Schema.Schemas = nil }, "schema.schemas"},
		"12 max_retries":             {func(c *Config) { c.TX.MaxRetries = -1 }, "max_retries"},
		"13 max page size":           {func(c *Config) { c.Limits.MaxPageSize = 0 }, "limits.max_page_size"},
		"14 log level":               {func(c *Config) { c.Log.Level = "loud" }, "log.level"},
		"15a base_path no slash":     {func(c *Config) { c.Server.BasePath = "api" }, "base_path"},
		"15b base_path trailing":     {func(c *Config) { c.Server.BasePath = "/api/" }, "base_path"},
		"16b transactions.end":       {func(c *Config) { c.TX.End = "maybe" }, "transactions.end"},
		"16 max depth":               {func(c *Config) { c.Limits.MaxDepth = 0 }, "limits.max_depth"},
		"25a max body bytes":         {func(c *Config) { c.Limits.MaxBodyBytes = 0 }, "limits.max_body_bytes"},
		"25b max cost":               {func(c *Config) { c.Limits.MaxCost = 0 }, "limits.max_cost"},
		"26 partial max_items":       {func(c *Config) { c.TX.Partial.MaxItems = 0 }, "transactions.partial.max_items"},
		"27a server_url relative":    {func(c *Config) { c.OpenAPI.ServerURL = "/api" }, "openapi.server_url"},
		"27b server_url query":       {func(c *Config) { c.OpenAPI.ServerURL = "https://x.example?a=1" }, "openapi.server_url"},
		"17a anon role none":         {func(c *Config) { c.Roles.Anonymous = "none" }, "roles.anonymous"},
		"17b default role NONE":      {func(c *Config) { c.Roles.Default = "NONE" }, "roles.default"},
		"17c allowed role none":      {func(c *Config) { c.Roles.Allowed = []string{"web_user", "None"} }, "roles.allowed"},
		"17d anon role public":       {func(c *Config) { c.Roles.Anonymous = "public" }, "roles.anonymous"},
		"17e default role pg_":       {func(c *Config) { c.Roles.Default = "pg_read_all_data" }, "roles.default"},
		"17f role not ident":         {func(c *Config) { c.Roles.Anonymous = "web anon" }, "roles.anonymous"},
		"18a claims prefix":          {func(c *Config) { c.Claims.Prefix = "plpgsql.claims" }, "claims.prefix"},
		"18b claims prefix pg_":      {func(c *Config) { c.Claims.Prefix = "pg_x.claims" }, "claims.prefix"},
		"18c claims prefix syn":      {func(c *Config) { c.Claims.Prefix = "a..b" }, "claims.prefix"},
		"19a metrics path":           {func(c *Config) { c.Metrics.Path = "metrics" }, "metrics.path"},
		"19b metrics path root":      {func(c *Config) { c.Metrics.Path = "/" }, "metrics.path"},
		"19c metrics path query":     {func(c *Config) { c.Metrics.Path = "/metrics?x=1" }, "metrics.path"},
		"20a metrics addr":           {func(c *Config) { c.Metrics.Addr = "9090" }, "metrics.addr"},
		"20b metrics addr main":      {func(c *Config) { c.Metrics.Addr = ":8080" }, "metrics.addr"},
		"21a metrics healthz":        {func(c *Config) { c.Metrics.Enabled = true; c.Metrics.Path = "/healthz" }, "collides"},
		"21b metrics readyz":         {func(c *Config) { c.Metrics.Enabled = true; c.Metrics.Path = "/readyz" }, "collides"},
		"21c metrics base_path":      {func(c *Config) { c.Metrics.Enabled = true; c.Server.BasePath = "/api"; c.Metrics.Path = "/api" }, "collides"},
		"21d metrics under base":     {func(c *Config) { c.Metrics.Enabled = true; c.Server.BasePath = "/api"; c.Metrics.Path = "/api/metrics" }, "collides"},
		"21e metrics openapi": {func(c *Config) {
			c.Metrics.Enabled = true
			c.Server.BasePath = ""
			c.Metrics.Path = "/openapi.json"
		}, "collides"},
		"22a contract name":    {func(c *Config) { c.Contracts = []confx.Contract{{Name: "Public"}} }, "contracts[0].name"},
		"22b contract overlap": {func(c *Config) { c.Contracts = []confx.Contract{{Name: "a"}, {Name: "b", Path: "/a/b"}} }, "overlaps"},
		"22c contract root":    {func(c *Config) { c.Contracts = []confx.Contract{{Name: "a", Path: "/"}, {Name: "b"}} }, "overlaps"},
		"22d contract smart_comments": {func(c *Config) {
			c.Contracts = []confx.Contract{{Name: "a"}}
			c.Plugins.Disabled = []string{"smart_comments"}
		}, "contracts require the smart_comments plugin"},
		"22e contract metrics": {func(c *Config) {
			c.Contracts = []confx.Contract{{Name: "a"}}
			c.Metrics.Enabled = true
			c.Metrics.Path = "/a/metrics"
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
		"switch off":      func(c *Config) { c.Roles.Switch = false },
		"mode none anon":  func(c *Config) { c.Auth.Mode = auth.ModeNone; c.Auth.JWT.Secret = ""; c.Roles.Anonymous = "anonymous" },
		"mode none conn":  func(c *Config) { c.Auth.Mode = auth.ModeNone; c.Auth.JWT.Secret = ""; c.Roles.Switch = false },
		"jwks only":       func(c *Config) { c.Auth.JWT.Secret = ""; c.Auth.JWT.JWKSURL = "https://idp.example.com/jwks" },
		"headers mode": func(c *Config) {
			c.Auth.Mode = auth.ModeHeaders
			c.Auth.Headers.TrustedProxies = []string{"10.0.0.0/8"}
		},
		"json setting":      func(c *Config) { c.Claims.JSONSetting = "request.jwt.claims" },
		"no claims prefix":  func(c *Config) { c.Claims.Prefix = "" },
		"disable built-ins": func(c *Config) { c.Plugins.Disabled = BuiltinPlugins },
		"log json":          func(c *Config) { c.Log.Format = "json" },
		"metrics on":        func(c *Config) { c.Metrics.Enabled = true },
		"metrics apiish":    func(c *Config) { c.Metrics.Enabled = true; c.Metrics.Path = "/apis/metrics" },
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
	cfg.Limits.MaxDepth = 0
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, s := range []string{"errors.detail", "log.level", "limits.max_depth"} {
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
auth:
  mode: none
roles:
  switch: false
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
	fs.Int("limits.max_page_size", 0, "")
	if err := fs.Parse([]string{"--server.addr=:5555"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, cliflags.Changed(fs, "config"))
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
	if cfg.Limits.MaxPageSize != 100 {
		t.Errorf("unset flag clobbered default: %d", cfg.Limits.MaxPageSize)
	}
	if cfg.Limits.MaxDepth != 5 {
		t.Errorf("default lost: %d", cfg.Limits.MaxDepth)
	}
}

func writeConfig(t *testing.T, yaml string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pdbr.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadStrict(t *testing.T) {
	for name, tc := range map[string]struct {
		yaml string
		env  map[string]string
		want string
	}{
		"unknown key":      {yaml: "server:\n  adr: \":1\"\n", want: "unknown key server.adr: did you mean server.addr?"},
		"unknown section":  {yaml: "graphiql: true\n", want: "unknown key graphiql"},
		"moved rls key":    {yaml: "rls:\n  enabled: false\n", want: "unknown key rls.enabled: moved to roles.switch"},
		"moved rls auth":   {yaml: "rls:\n  auth:\n    jwt_secret: x\n", want: "unknown key rls.auth.jwt_secret: moved to auth.jwt.secret"},
		"moved plugin":     {yaml: "plugins:\n  settings:\n    advanced-filters:\n      relations: false\n", want: "moved to plugins.settings.advanced_filters.relations"},
		"moved nested":     {yaml: "plugins:\n  settings:\n    nested-mutations:\n      max_depth: 2\n", want: "moved to plugins.settings.nested_mutations.max_depth"},
		"unknown setting":  {yaml: "plugins:\n  settings:\n    postgis:\n      aggregate: false\n", want: "did you mean plugins.settings.postgis.aggregates?"},
		"unknown env":      {env: map[string]string{"PDBR_SERVER_ADR": ":1"}, want: "unknown environment variable PDBR_SERVER_ADR"},
		"moved env":        {env: map[string]string{"PDBR_RLS_ENABLED": "false"}, want: "PDBR_RLS_ENABLED (rls.enabled): moved to PDBR_ROLES_SWITCH"},
		"moved env secret": {env: map[string]string{"PDBR_RLS_AUTH_JWT__SECRET": "x"}, want: "moved to PDBR_AUTH_JWT_SECRET"},
		"mistyped setting": {yaml: "plugins:\n  settings:\n    nested_mutations:\n      max_depth: deep\n", want: "max_depth"},
		"kebab disabled":   {yaml: "plugins:\n  disabled: [smart-comments]\n", want: `unknown plugin "smart-comments", did you mean "smart_comments"?`},
		"unknown disabled": {yaml: "plugins:\n  disabled: [simple_names]\n", want: `unknown plugin "simple_names"`},
		"log format":       {yaml: "log:\n  format: pretty\n", want: "log.format"},
	} {
		t.Run(name, func(t *testing.T) {
			path := ""
			if tc.yaml != "" {
				path = writeConfig(t, "auth:\n  mode: none\nroles:\n  switch: false\n"+tc.yaml)
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			_, err := Load(path, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
	t.Run("unrelated env ignored", func(t *testing.T) {
		t.Setenv("PDBR_TEST_DATABASE_URL", "postgres://x")
		t.Setenv("PDBR_TEST_PDBQ_DIR", "/x")
		if _, err := Load(writeConfig(t, "auth:\n  mode: none\nroles:\n  switch: false\n"), nil); err != nil {
			t.Fatal(err)
		}
	})
}

func TestLoadPluginSettingsFromEnv(t *testing.T) {
	t.Setenv("PDBR_AUTH_MODE", "none")
	t.Setenv("PDBR_ROLES_SWITCH", "false")
	t.Setenv("PDBR_PLUGINS_SETTINGS_ADVANCED__FILTERS_RELATIONS", "false")
	t.Setenv("PDBR_PLUGINS_SETTINGS_NESTED__MUTATIONS_MAX__DEPTH", "7")
	t.Setenv("PDBR_PLUGINS_SETTINGS_POSTGIS_AGGREGATES", "false")
	t.Setenv("PDBR_PLUGINS_DISABLED", "smart_comments,postgis")
	cfg, err := Load("", nil)
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.Plugins.Settings
	if s.AdvancedFilters.Relations || !s.AdvancedFilters.Computed || s.NestedMutations.MaxDepth != 7 || s.PostGIS.Aggregates || !s.PostGIS.Filters {
		t.Fatalf("plugins.settings = %+v", s)
	}
	if !reflect.DeepEqual(cfg.Plugins.Disabled, []string{"smart_comments", "postgis"}) {
		t.Fatalf("plugins.disabled = %v", cfg.Plugins.Disabled)
	}
	if cfg.Auth.Mode != auth.ModeNone || cfg.Roles.Switch {
		t.Fatalf("auth/roles from env = %+v %+v", cfg.Auth, cfg.Roles)
	}
}

func TestEnvKeyMapper(t *testing.T) {
	m := confx.EnvKeyMapper(EnvPrefix)
	cases := map[string]string{
		"PDBR_DATABASE_URL":                                 "database.url",
		"PDBR_SERVER_BASE__PATH":                            "server.base_path",
		"PDBR_SERVER_MAX__PAGE__SIZE":                       "server.max_page_size",
		"PDBR_AUTH_JWT_SECRET":                              "auth.jwt.secret",
		"PDBR_ROLES_ANONYMOUS":                              "roles.anonymous",
		"PDBR_PLUGINS_SETTINGS_ADVANCED__FILTERS_RELATIONS": "plugins.settings.advanced_filters.relations",
		"PDBR_FILTERS_INDEXED__ONLY":                        "filters.indexed_only",
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
	if !strings.Contains(out, "base_path: \"\"") || !strings.Contains(out, "title: \"pdbr\"") {
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
	if got := cfg.ContractBase(confx.Contract{Name: "public"}); got != "/public" {
		t.Errorf("ContractBase(public) at the root = %q", got)
	}
	cfg.Server.BasePath = "/api"
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

func TestLoadRegroupedKeysMoved(t *testing.T) {
	values := map[string]string{
		"server.max_body_bytes":             "10",
		"server.max_embed_depth":            "2",
		"server.max_cost":                   "10",
		"server.max_page_size":              "10",
		"server.max_headline_document":      "10",
		"server.tx_end":                     "rollback",
		"server.partial_bulk_writes":        "true",
		"server.partial_bulk_max_items":     "10",
		"server.openapi":                    "false",
		"server.openapi_domains":            "false",
		"server.allow_unfiltered_mutations": "true",
		"server.aggregates":                 "true",
	}
	for old, to := range MovedKeys {
		if !strings.HasPrefix(old, "server.") {
			continue
		}
		v, ok := values[old]
		if !ok {
			t.Errorf("no test value for moved key %s", old)
			continue
		}
		t.Run(old, func(t *testing.T) {
			leaf := strings.TrimPrefix(old, "server.")
			path := writeConfig(t, "auth:\n  mode: none\nroles:\n  switch: false\nserver:\n  "+leaf+": "+v+"\n")
			_, err := Load(path, nil)
			if want := "unknown key " + old + ": moved to " + to; err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("yaml: err = %v, want %q", err, want)
			}
			env := "PDBR_SERVER_" + strings.ToUpper(strings.ReplaceAll(leaf, "_", "__"))
			t.Setenv("PDBR_AUTH_MODE", "none")
			t.Setenv("PDBR_ROLES_SWITCH", "false")
			t.Setenv(env, v)
			newEnv := "PDBR_" + strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(to, "_", "__"), ".", "_"))
			_, err = Load("", nil)
			if want := env + " (" + old + "): moved to " + newEnv; err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("env: err = %v, want %q", err, want)
			}
		})
	}
}

func TestLoadRemovedKeys(t *testing.T) {
	for name, yaml := range map[string]string{
		"transactions.mutations":                               "transactions:\n  mutations: true\n",
		"plugins.settings.nested_mutations.force_transactions": "plugins:\n  settings:\n    nested_mutations:\n      force_transactions: true\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeConfig(t, "auth:\n  mode: none\nroles:\n  switch: false\n"+yaml), nil)
			if err == nil || !strings.Contains(err.Error(), "unknown key "+name+": removed: "+RemovedKeys[name]) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	t.Setenv("PDBR_AUTH_MODE", "none")
	t.Setenv("PDBR_ROLES_SWITCH", "false")
	t.Setenv("PDBR_TRANSACTIONS_MUTATIONS", "false")
	if _, err := Load("", nil); err == nil || !strings.Contains(err.Error(), "unknown environment variable PDBR_TRANSACTIONS_MUTATIONS (transactions.mutations): removed: every write runs in a transaction") {
		t.Fatalf("env err = %v", err)
	}
}

func TestLoadRegroupedKeys(t *testing.T) {
	path := writeConfig(t, `
auth:
  mode: none
roles:
  switch: false
openapi:
  enabled: false
  title: Orders API
  server_url: https://api.example.com
limits:
  max_body_bytes: 2048
  max_depth: 3
  max_cost: 500
  max_page_size: 20
  max_headline_document: 100
schema:
  domains: false
mutations:
  allow_unfiltered: true
aggregates:
  enabled: true
transactions:
  end: rollback-allow-override
  partial:
    enabled: true
    max_items: 50
`)
	cfg, err := Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OpenAPI != (OpenAPI{Title: "Orders API", ServerURL: "https://api.example.com"}) {
		t.Errorf("openapi = %+v", cfg.OpenAPI)
	}
	if cfg.Limits != (Limits{MaxBodyBytes: 2048, MaxDepth: 3, MaxCost: 500, MaxPageSize: 20, MaxHeadlineDocument: 100}) {
		t.Errorf("limits = %+v", cfg.Limits)
	}
	if cfg.Schema.Domains || !cfg.Mutations.AllowUnfiltered || !cfg.Aggregates.Enabled {
		t.Errorf("schema.domains / mutations / aggregates = %v %+v %+v", cfg.Schema.Domains, cfg.Mutations, cfg.Aggregates)
	}
	if cfg.TX.End != "rollback-allow-override" || cfg.TX.Partial != (Partial{Enabled: true, MaxItems: 50}) {
		t.Errorf("transactions = %+v", cfg.TX)
	}

	for k, v := range map[string]string{
		"PDBR_AUTH_MODE":                       "none",
		"PDBR_ROLES_SWITCH":                    "false",
		"PDBR_OPENAPI_ENABLED":                 "false",
		"PDBR_OPENAPI_TITLE":                   "Env API",
		"PDBR_OPENAPI_SERVER__URL":             "https://env.example.com",
		"PDBR_LIMITS_MAX__DEPTH":               "4",
		"PDBR_LIMITS_MAX__COST":                "700",
		"PDBR_SCHEMA_DOMAINS":                  "false",
		"PDBR_MUTATIONS_ALLOW__UNFILTERED":     "true",
		"PDBR_AGGREGATES_ENABLED":              "true",
		"PDBR_TRANSACTIONS_END":                "rollback",
		"PDBR_TRANSACTIONS_PARTIAL_ENABLED":    "true",
		"PDBR_TRANSACTIONS_PARTIAL_MAX__ITEMS": "200",
	} {
		t.Setenv(k, v)
	}
	cfg, err = Load("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OpenAPI.Enabled || cfg.OpenAPI.Title != "Env API" || cfg.OpenAPI.ServerURL != "https://env.example.com" ||
		cfg.Limits.MaxDepth != 4 || cfg.Limits.MaxCost != 700 || cfg.Schema.Domains || !cfg.Mutations.AllowUnfiltered ||
		!cfg.Aggregates.Enabled || cfg.TX.End != "rollback" || cfg.TX.Partial != (Partial{Enabled: true, MaxItems: 200}) {
		t.Errorf("env config = %+v %+v %+v %+v %+v", cfg.OpenAPI, cfg.Limits, cfg.Mutations, cfg.Aggregates, cfg.TX)
	}

	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.String("transactions.end", "", "")
	fs.Int("transactions.partial.max_items", 0, "")
	fs.Bool("openapi.enabled", true, "")
	if err := fs.Parse([]string{"--transactions.end=commit", "--transactions.partial.max_items=9", "--openapi.enabled=true"}); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load("", cliflags.Changed(fs, "config"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TX.End != "commit" || cfg.TX.Partial.MaxItems != 9 || !cfg.OpenAPI.Enabled {
		t.Errorf("flags = %+v %+v", cfg.TX, cfg.OpenAPI)
	}
}

func TestLoadDoubledUnderscoreAfterSection(t *testing.T) {
	t.Setenv("PDBR_AUTH_MODE", "none")
	t.Setenv("PDBR_ROLES_SWITCH", "false")
	t.Setenv("PDBR_MUTATIONS__ALLOW__UNFILTERED", "true")
	_, err := Load("", nil)
	if want := "unknown environment variable PDBR_MUTATIONS__ALLOW__UNFILTERED (mutations_allow_unfiltered): did you mean PDBR_MUTATIONS_ALLOW__UNFILTERED?"; err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

func TestValidateSharedDurations(t *testing.T) {
	cfg := DefaultConfig()
	cfg.UseConnectionIdentity()
	cfg.Database.ConnectTimeout = 0
	cfg.Watch.Enabled = true
	cfg.Watch.PollInterval = 0
	err := cfg.Validate()
	for _, want := range []string{"database.connect_timeout must be > 0", "watch.poll_interval must be > 0"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want %q", err, want)
		}
	}
}
