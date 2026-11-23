package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/cache"
	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/config"
)

func TestBuildRequestPaths(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.BasePath = "/api"
	cases := map[string]string{
		"/users?select=id":     "/api/users?select=id",
		"users":                "/api/users",
		"/api/users?select=id": "/api/users?select=id",
		"/api":                 "/api",
		"/healthz":             "/healthz",
		"/readyz":              "/readyz",
		"/openapi.json":        "/api/openapi.json",
	}
	for in, want := range cases {
		r, err := buildRequest(context.Background(), cfg, requestSpec{method: "GET", path: in})
		if err != nil {
			t.Fatal(err)
		}
		if got := r.URL.RequestURI(); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
	cfg.Server.BasePath = ""
	r, _ := buildRequest(context.Background(), cfg, requestSpec{method: "GET", path: "/users"})
	if r.URL.Path != "/users" {
		t.Errorf("root base path: %q", r.URL.Path)
	}
	r, err := buildRequest(context.Background(), cfg, requestSpec{method: "POST", path: "/users", body: `{"a":1}`,
		headers: []string{"Prefer: return=minimal", "Accept: application/json"}})
	if err != nil || r.Header.Get("Content-Type") != "application/json" || r.ContentLength != 7 || r.Header.Get("Prefer") != "return=minimal" {
		t.Errorf("body/headers: %v %v %d", err, r.Header, r.ContentLength)
	}
	if _, err := buildRequest(context.Background(), cfg, requestSpec{method: "GET", path: "/users", headers: []string{"bad"}}); err == nil {
		t.Error("malformed header accepted")
	}
}

// TestPrintSQLMatchesGolden pins `pdbr request --print-sql` to the golden
// format: for a few compile goldens the CLI output over a schema cache of
// the fixture catalog is byte-identical to the checked-in .sql file.
func TestPrintSQLMatchesGolden(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "schema.cache")
	if err := cache.Save(cachePath, testutil.FixtureCatalog(), cache.Writer{Product: "pdbr", Version: "test"}); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Database.URL = "postgres://nobody@127.0.0.1:1/none"
	cfg.Server.BasePath = "/api"
	cfg.Schema.CachePath = cachePath
	cfg.UseConnectionIdentity()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"list_select", "filter_ops", "keyset_after", "create", "rpc_setof_table_paged"} {
		spec := readGolden(t, filepath.Join("..", "..", "internal", "compile", "testdata", name+".http"))
		want, err := os.ReadFile(filepath.Join("..", "..", "internal", "compile", "testdata", name+".sql"))
		if err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		spec.printSQL = true
		if err := runRequest(context.Background(), cfg, spec, &stdout, &stderr); err != nil {
			t.Fatalf("%s: %v (%s)", name, err, stdout.String())
		}
		if stdout.String() != string(want) {
			t.Errorf("%s: --print-sql differs from golden:\n%s\nwant:\n%s", name, stdout.String(), want)
		}
	}
	// Requests that never reach the compiler print a problem and exit 2.
	var stdout, stderr bytes.Buffer
	err := runRequest(context.Background(), cfg, requestSpec{method: "GET", path: "/openapi.json", printSQL: true}, &stdout, &stderr)
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 2 || !strings.Contains(stdout.String(), `"code":"not_found"`) || !strings.HasPrefix(stderr.String(), "HTTP/1.1 404") {
		t.Errorf("openapi.json --print-sql: err %v out %q err %q", err, stdout.String(), stderr.String())
	}
	stdout.Reset()
	err = runRequest(context.Background(), cfg, requestSpec{method: "POST", path: "/users/1", printSQL: true}, &stdout, &stderr)
	if !errors.As(err, &ee) || ee.code != 2 || !strings.Contains(stdout.String(), `"code":"method_not_allowed"`) {
		t.Errorf("405 --print-sql: err %v out %q", err, stdout.String())
	}
}

// readGolden parses a compile golden .http file into a requestSpec.
func readGolden(t *testing.T, path string) requestSpec {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	head, body, _ := strings.Cut(string(data), "\n\n")
	lines := strings.Split(strings.TrimRight(head, "\n"), "\n")
	method, target, _ := strings.Cut(lines[0], " ")
	spec := requestSpec{method: method, path: target, headers: lines[1:], body: strings.TrimSpace(body)}
	return spec
}

func TestFlagValueCompletion(t *testing.T) {
	for flag, want := range map[string]string{
		"--errors.detail": "dev\nprod\nstrict\n",
		"--log.level":     "debug\ninfo\nwarn\nerror\n",
	} {
		for _, sub := range [][]string{{"serve"}, {"request"}, {"schema", "print"}} {
			root := rootCmd()
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetArgs(append(append([]string{"__complete"}, sub...), flag, ""))
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(out.String(), want) {
				t.Errorf("%v %s completion = %q, want prefix %q", sub, flag, out.String(), want)
			}
		}
	}
	root := rootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"completion", "zsh"})
	if err := root.Execute(); err != nil || !strings.HasPrefix(out.String(), "#compdef pdbr") {
		t.Errorf("completion zsh: %v %q", err, out.String()[:min(len(out.String()), 40)])
	}
}

// TestCommandsRunOnDefaultConfig: pdbr request and pdbr plugins list need
// no JWT secret (they run as the connection role without credentials), and
// command options such as --print-sql are not taken for config keys.
func TestCommandsRunOnDefaultConfig(t *testing.T) {
	t.Chdir(t.TempDir())
	cachePath := filepath.Join(t.TempDir(), "schema.cache")
	if err := cache.Save(cachePath, testutil.FixtureCatalog(), cache.Writer{Product: "pdbr", Version: "test"}); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, "request", "GET", "/users?select=id", "--print-sql", "--schema.cache_path", cachePath, "--log.level", "error")
	if err != nil || !strings.Contains(out, "-- request: GET /users?select=id") {
		t.Fatalf("request --print-sql on defaults: %v\n%s", err, out)
	}
	if _, err := runCLI(t, "plugins", "list"); err != nil {
		t.Fatalf("plugins list on defaults: %v", err)
	}
	if _, err := runCLI(t, "config", "validate"); err == nil || !strings.Contains(err.Error(), "auth.jwt.secret") {
		t.Fatalf("config validate on defaults = %v, want the missing secret", err)
	}
	if _, err := runCLI(t, "request", "GET", "/users", "--rls.enabled=false"); err == nil || !strings.Contains(err.Error(), "unknown flag: --rls.enabled") {
		t.Fatalf("removed flag: %v", err)
	}
}
