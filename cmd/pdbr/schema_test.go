package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbcore/cache"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"
)

func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := rootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestSchemaCheckRejectsBadCacheBeforeConnecting(t *testing.T) {
	dir := t.TempDir()
	// database.url points nowhere: every case below must fail on the cache
	// file, never by trying to reach the database.
	base := []string{"schema", "check", "--database.url", "postgres://nobody:x@127.0.0.1:1/none"}

	_, err := runCLI(t, append(base, "--cache", filepath.Join(dir, "missing.cache"))...)
	if err == nil || !strings.Contains(err.Error(), "cache: open") {
		t.Errorf("missing cache: %v", err)
	}

	corrupt := filepath.Join(dir, "corrupt.cache")
	os.WriteFile(corrupt, []byte("not gzip"), 0o644)
	_, err = runCLI(t, append(base, "--cache", corrupt)...)
	if !errors.Is(err, cache.ErrCorrupt) {
		t.Errorf("corrupt cache: %v", err)
	}

	stale := filepath.Join(dir, "stale.cache")
	f, _ := os.Create(stale)
	zw := gzip.NewWriter(f)
	json.NewEncoder(zw).Encode(map[string]any{"format_version": introspect.CatalogFormatVersion - 1, "hash": "x", "catalog": testutil.FixtureCatalog()})
	zw.Close()
	f.Close()
	_, err = runCLI(t, append(base, "--cache", stale)...)
	var fv *cache.FormatVersionError
	if !errors.As(err, &fv) || fv.Got != introspect.CatalogFormatVersion-1 {
		t.Errorf("stale format: %v", err)
	}
}

func TestSchemaDriftReport(t *testing.T) {
	cached := testutil.FixtureCatalog()
	live := testutil.FixtureCatalog()
	same, err := schemaDrift(cached, live)
	if err != nil || same != "" {
		t.Fatalf("identical catalogs: %q %v", same, err)
	}
	live.Tables = append(live.Tables, &introspect.Table{Schema: "public", Name: "probe", Kind: "table"})
	live.Enums = live.Enums[1:]
	report, err := schemaDrift(cached, live)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(report, "\n")
	if !strings.HasPrefix(lines[0], "schema drift detected: cache ") || !strings.Contains(lines[0], " != live ") {
		t.Errorf("header = %q", lines[0])
	}
	if !strings.Contains(report, "\n  table public.probe added") || !strings.Contains(report, "\n  enum public.") {
		t.Errorf("report = %q", report)
	}
}

// TestSchemaDumpCheckLive: dump → check (clean) → DDL → check (drift, exit 1
// with the per-object report) → dump again → check (clean).
func TestSchemaDumpCheckLive(t *testing.T) {
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
	dropProbe := func() {
		c, err := pgx.Connect(ctx, dbURL)
		if err != nil {
			t.Log(err)
			return
		}
		defer c.Close(ctx)
		c.Exec(ctx, `DROP TABLE IF EXISTS public.cache_probe`)
	}
	dropProbe()
	t.Cleanup(dropProbe)

	path := filepath.Join(t.TempDir(), "schema.cache")
	flags := []string{"--database.url", dbURL, "--auth.mode=none", "--roles.switch=false"}
	out, err := runCLI(t, append([]string{"schema", "dump", "-o", path}, flags...)...)
	if err != nil || !strings.HasPrefix(out, "wrote "+path) {
		t.Fatalf("dump: %v %q", err, out)
	}
	if _, err := cache.Load(path); err != nil {
		t.Fatalf("dump wrote an unloadable cache: %v", err)
	}
	// dump ignores schema.cache_path: it must introspect live even when told
	// to boot from a (bogus) cache.
	out, err = runCLI(t, append([]string{"schema", "dump", "-o", path, "--schema.cache_path", filepath.Join(t.TempDir(), "nope")}, flags...)...)
	if err != nil || !strings.HasPrefix(out, "wrote ") {
		t.Fatalf("dump with cache_path: %v %q", err, out)
	}

	out, err = runCLI(t, append([]string{"schema", "check", "--cache", path}, flags...)...)
	if err != nil || !strings.Contains(out, "schema cache is up to date") {
		t.Fatalf("check clean: %v %q", err, out)
	}

	if _, err := conn.Exec(ctx, `CREATE TABLE public.cache_probe (id int PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	out, err = runCLI(t, append([]string{"schema", "check", "--cache", path}, flags...)...)
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 1 {
		t.Fatalf("check drift: err = %v, want exit 1", err)
	}
	if !strings.HasPrefix(out, "schema drift detected: cache ") || !strings.Contains(out, "\n  table public.cache_probe added") {
		t.Fatalf("drift report = %q", out)
	}

	if _, err := runCLI(t, append([]string{"schema", "dump", "-o", path}, flags...)...); err != nil {
		t.Fatal(err)
	}
	if out, err = runCLI(t, append([]string{"schema", "check", "--cache", path}, flags...)...); err != nil {
		t.Fatalf("check after re-dump: %v %q", err, out)
	}
}
