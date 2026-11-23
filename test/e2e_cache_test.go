package e2e

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/suprbdev/pdbcore/cache"

	"github.com/suprbdev/pdbr"
	"github.com/suprbdev/pdbr/internal/config"
)

// dumpCache introspects the live database the way `pdbr schema dump` does
// and writes the cache file; returns the path and the catalog hash.
func dumpCache(t *testing.T, dbURL string) (string, string) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Database.URL = dbURL
	cfg.UseConnectionIdentity()
	app := pdbr.New(cfg)
	defer app.Close()
	cat, err := app.LoadCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "schema.cache")
	if err := cache.Save(path, cat, cache.Writer{Product: "pdbr", Version: "test"}); err != nil {
		t.Fatal(err)
	}
	hash, _ := cat.Hash()
	return path, hash
}

// TestCacheBootWithoutDatabase: a server booted from a cache never touches
// pg_catalog — with an unreachable database it still serves the same OpenAPI
// document as a live introspection, and only data requests fail (503).
func TestCacheBootWithoutDatabase(t *testing.T) {
	live := testServer(t, nil)
	_, liveDoc := get(t, live.URL+"/api/openapi.json")

	path, _ := dumpCache(t, os.Getenv("PDBR_TEST_DATABASE_URL"))
	if _, err := cache.Load(path); err != nil {
		t.Fatal(err)
	}

	offline := testServer(t, func(c *config.Config) {
		c.Schema.CachePath = path
		c.Database.URL = "postgres://nobody:x@127.0.0.1:1/none?connect_timeout=1"
	})
	res, doc := get(t, offline.URL+"/api/openapi.json")
	if res.StatusCode != 200 || !bytes.Equal(doc, liveDoc) {
		t.Fatalf("cache-booted OpenAPI differs from live (%d, %d vs %d bytes)", res.StatusCode, len(doc), len(liveDoc))
	}
	res, body := get(t, offline.URL+"/api/users?limit=1")
	wantProblem(t, res, body, 503, "unavailable")
	res, body = get(t, offline.URL+"/readyz")
	wantProblem(t, res, body, 503, "unavailable")
	if res.Header.Get("Retry-After") != "1" {
		t.Errorf("readyz Retry-After = %q", res.Header.Get("Retry-After"))
	}
	if res, _ = get(t, offline.URL+"/healthz"); res.StatusCode != 200 {
		t.Errorf("healthz with the database down = %d", res.StatusCode)
	}

	// Same cache with the real database: data requests work and the
	// document is still the live one.
	cached := testServer(t, func(c *config.Config) { c.Schema.CachePath = path })
	var rows []row
	getJSON(t, cached.URL+"/api/users?select=id&limit=1", &rows)
	if len(rows) != 1 {
		t.Fatalf("rows = %v", rows)
	}
	if _, doc := get(t, cached.URL+"/api/openapi.json"); !bytes.Equal(doc, liveDoc) {
		t.Fatal("cache-booted OpenAPI (with DB) differs from live")
	}
}

// TestCacheInteropPdbq: a cache written by `pdbq schema dump` boots pdbr and
// hashes equal to pdbr's own introspection. Needs PDBR_TEST_PDBQ_DIR pointing
// at a pdbq checkout (its binary is built with `go run`).
func TestCacheInteropPdbq(t *testing.T) {
	dbURL := os.Getenv("PDBR_TEST_DATABASE_URL")
	pdbqDir := os.Getenv("PDBR_TEST_PDBQ_DIR")
	if dbURL == "" || pdbqDir == "" {
		t.Skip("PDBR_TEST_DATABASE_URL and PDBR_TEST_PDBQ_DIR required")
	}
	path := filepath.Join(t.TempDir(), "pdbq.cache")
	cmd := exec.Command("go", "run", "./cmd/pdbq", "schema", "dump", "-o", path, "--database.url", dbURL)
	cmd.Dir = pdbqDir
	cmd.Env = append(os.Environ(), "PDBQ_AUTH_MODE=none", "PDBQ_ROLES_SWITCH=false")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pdbq schema dump: %v\n%s", err, out)
	}
	cat, err := cache.Load(path)
	if err != nil {
		t.Fatalf("pdbr cannot load pdbq's cache: %v", err)
	}
	pdbqHash, _ := cat.Hash()
	_, pdbrHash := dumpCache(t, dbURL)
	if pdbqHash != pdbrHash {
		t.Fatalf("catalog hash: pdbq %s != pdbr %s (schema check would report drift)", pdbqHash[:12], pdbrHash[:12])
	}

	live := testServer(t, nil)
	_, liveDoc := get(t, live.URL+"/api/openapi.json")
	fromPdbq := testServer(t, func(c *config.Config) { c.Schema.CachePath = path })
	if _, doc := get(t, fromPdbq.URL+"/api/openapi.json"); !bytes.Equal(doc, liveDoc) {
		t.Fatal("OpenAPI from pdbq's cache differs from live introspection")
	}
	var rows []row
	getJSON(t, fromPdbq.URL+"/api/posts?select=id&limit=2", &rows)
	if len(rows) != 2 {
		t.Fatalf("rows = %v", rows)
	}
}
