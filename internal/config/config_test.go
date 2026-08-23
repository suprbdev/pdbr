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
		"server.addr":              ":8080",
		"server.base_path":         "/api",
		"server.openapi":           true,
		"server.request_timeout":   30 * time.Second,
		"server.max_body_bytes":    int64(1 << 20),
		"server.max_embed_depth":   5,
		"server.max_cost":          10000,
		"server.max_page_size":     100,
		"server.etags":             true,
		"server.csv":               true,
		"server.compression":       false,
		"server.read_only":         false,
		"rls.claims_prefix":        "pdbq.claims",
		"rls.auth.header_prefix":   "X-Pdbr-Claim-",
		"rls.anonymous_role":       "anonymous",
		"watch.channel":            "pdbr_ddl",
		"database.max_conns":       int32(10),
		"transactions.isolation":   "read_committed",
		"transactions.max_retries": 0,
		"errors.detail":            "prod",
	}
	got := map[string]any{
		"server.addr":              cfg.Server.Addr,
		"server.base_path":         cfg.Server.BasePath,
		"server.openapi":           cfg.Server.OpenAPI,
		"server.request_timeout":   cfg.Server.RequestTimeout,
		"server.max_body_bytes":    cfg.Server.MaxBodyBytes,
		"server.max_embed_depth":   cfg.Server.MaxEmbedDepth,
		"server.max_cost":          cfg.Server.MaxCost,
		"server.max_page_size":     cfg.Server.MaxPageSize,
		"server.etags":             cfg.Server.Etags,
		"server.csv":               cfg.Server.CSV,
		"server.compression":       cfg.Server.Compression,
		"server.read_only":         cfg.Server.ReadOnly,
		"rls.claims_prefix":        cfg.RLS.ClaimsPrefix,
		"rls.auth.header_prefix":   cfg.RLS.Auth.HeaderPrefix,
		"rls.anonymous_role":       cfg.RLS.AnonymousRole,
		"watch.channel":            cfg.Watch.Channel,
		"database.max_conns":       cfg.Database.MaxConns,
		"transactions.isolation":   cfg.TX.Isolation,
		"transactions.max_retries": cfg.TX.MaxRetries,
		"errors.detail":            cfg.Errors.Detail,
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
		"16 embed depth":         {func(c *Config) { c.Server.MaxEmbedDepth = 0 }, "max_embed_depth"},
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
