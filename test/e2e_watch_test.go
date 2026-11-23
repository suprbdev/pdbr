package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbr"
	"github.com/suprbdev/pdbr/internal/config"
)

// watchServer boots an App in watch mode and returns once its watcher is
// ready, so DDL issued afterwards is always observed.
func watchServer(t *testing.T, dbURL string, logs *syncWriter, mutate func(*config.Config)) *httptest.Server {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Database.URL = dbURL
	cfg.Server.BasePath = "/api"
	cfg.UseConnectionIdentity()
	cfg.Errors.Detail = "dev"
	cfg.Watch.Enabled = true
	if mutate != nil {
		mutate(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	opts := []pdbr.Option{pdbr.WithVersion("e2e")}
	if logs != nil {
		opts = append(opts, pdbr.WithLogger(slog.New(slog.NewTextHandler(logs, nil))))
	}
	app := pdbr.New(cfg, opts...)
	t.Cleanup(app.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h, err := app.Handler(ctx)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	select {
	case <-app.WatchReady():
	case <-time.After(15 * time.Second):
		t.Fatal("watcher never became ready")
	}
	return srv
}

type syncWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func execDDL(t *testing.T, dbURL string, stmts ...string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	for _, s := range stmts {
		if _, err := conn.Exec(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

// openAPIProperties returns the property names of components.schemas[name]
// ("" when the schema is absent).
func openAPIProperties(t *testing.T, srv *httptest.Server, name string) []string {
	t.Helper()
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	getJSON(t, srv.URL+"/api/openapi.json", &doc)
	s, ok := doc.Components.Schemas[name]
	if !ok {
		return nil
	}
	props := make([]string, 0, len(s.Properties))
	for k := range s.Properties {
		props = append(props, k)
	}
	return props
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// TestWatchEventTrigger: with a superuser connection the watcher installs
// the pdbr_watch event trigger; CREATE TABLE / ALTER TABLE ADD COLUMN show up
// in the routes and the OpenAPI document without a restart.
func TestWatchEventTrigger(t *testing.T) {
	dbURL := os.Getenv("PDBR_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	execDDL(t, dbURL, `DROP TABLE IF EXISTS public.watch_probe`)
	t.Cleanup(func() { execDDL(t, dbURL, `DROP TABLE IF EXISTS public.watch_probe`) })

	logs := &syncWriter{}
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(logs.String())
		}
	})
	srv := watchServer(t, dbURL, logs, func(c *config.Config) { c.Metrics.Enabled = true })
	if res, _ := get(t, srv.URL+"/api/watch_probe"); res.StatusCode != 404 {
		t.Fatalf("watch_probe before create = %d", res.StatusCode)
	}

	execDDL(t, dbURL, `CREATE TABLE public.watch_probe (id serial PRIMARY KEY, name text NOT NULL)`,
		`INSERT INTO public.watch_probe (name) VALUES ('a')`)
	waitFor(t, "new table to appear", 15*time.Second, func() bool {
		res, _ := get(t, srv.URL+"/api/watch_probe")
		return res.StatusCode == 200
	})
	if props := openAPIProperties(t, srv, "watch_probe"); !contains(props, "name") || contains(props, "note") {
		t.Fatalf("openapi properties after create = %v", props)
	}

	execDDL(t, dbURL, `ALTER TABLE public.watch_probe ADD COLUMN note text`)
	waitFor(t, "new column in openapi", 15*time.Second, func() bool {
		return contains(openAPIProperties(t, srv, "watch_probe"), "note")
	})
	var rows []row
	getJSON(t, srv.URL+"/api/watch_probe?select=id,name,note", &rows)
	if len(rows) != 1 || rows[0]["name"] != "a" || rows[0]["note"] != nil {
		t.Fatalf("rows after alter = %v", rows)
	}
	out := scrapeMetrics(t, srv.URL+"/metrics")
	if got := metricValue(out, `pdbr_schema_rebuilds_total{result="success"}`); got < 2 {
		t.Errorf("pdbr_schema_rebuilds_total{result=\"success\"} = %v, want >= 2", got)
	}
	if got := metricValue(out, `pdbr_schema_last_rebuild_timestamp_seconds`); got <= 0 {
		t.Errorf("pdbr_schema_last_rebuild_timestamp_seconds = %v, want > 0", got)
	}

	// The event trigger and its function are pdbr's own names (shared DBs
	// with pdbq keep separate objects).
	execDDL(t, dbURL, `DO $$ BEGIN
		IF NOT EXISTS (SELECT 1 FROM pg_event_trigger WHERE evtname = 'pdbr_watch') THEN RAISE EXCEPTION 'pdbr_watch trigger missing'; END IF;
		IF NOT EXISTS (SELECT 1 FROM pg_proc WHERE proname = 'pdbr_watch_notify') THEN RAISE EXCEPTION 'pdbr_watch_notify missing'; END IF;
	END $$`)
}

// TestWatchPollFallback: a role that cannot create event triggers (not a
// superuser) makes the watcher fall back to hash polling; DDL performed by
// the superuser is still picked up.
func TestWatchPollFallback(t *testing.T) {
	dbURL := os.Getenv("PDBR_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	const role, pass = "e2e_watch_poll", "e2e_watch_poll"
	execDDL(t, dbURL,
		`DROP TABLE IF EXISTS public.watch_poll_probe`,
		fmt.Sprintf(`DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '%s') THEN
				CREATE ROLE %s LOGIN NOSUPERUSER PASSWORD '%s';
			END IF;
		END $$`, role, role, pass),
		`GRANT pg_read_all_data TO `+role,
	)
	t.Cleanup(func() { execDDL(t, dbURL, `DROP TABLE IF EXISTS public.watch_poll_probe`) })

	u, err := url.Parse(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(role, pass)
	pollURL := u.String()

	logs := &syncWriter{}
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(logs.String())
		}
	})
	srv := watchServer(t, pollURL, logs, func(c *config.Config) { c.Watch.PollInterval = 300 * time.Millisecond })
	if res, _ := get(t, srv.URL+"/api/watch_poll_probe"); res.StatusCode != 404 {
		t.Fatalf("probe before create = %d", res.StatusCode)
	}
	execDDL(t, dbURL, `CREATE TABLE public.watch_poll_probe (id serial PRIMARY KEY, v text)`)
	waitFor(t, "poll fallback to pick up the new table", 15*time.Second, func() bool {
		res, _ := get(t, srv.URL+"/api/watch_poll_probe")
		return res.StatusCode == 200
	})

	if props := openAPIProperties(t, srv, "watch_poll_probe"); !contains(props, "v") {
		t.Fatalf("openapi properties = %v", props)
	}
}

// TestCloseStopsWatcher: App.Close returns without the context given to
// Handler being cancelled, and is idempotent.
func TestCloseStopsWatcher(t *testing.T) {
	dbURL := os.Getenv("PDBR_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	cfg := config.DefaultConfig()
	cfg.Database.URL = dbURL
	cfg.UseConnectionIdentity()
	cfg.Watch.Enabled = true
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	app := pdbr.New(cfg, pdbr.WithVersion("e2e"))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h, err := app.Handler(ctx)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	done := make(chan struct{})
	go func() {
		app.Close()
		app.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("App.Close did not return while the watcher context was live")
	}
}

// TestCloseWithoutServe: Close is safe on an App that never started a server.
func TestCloseWithoutServe(t *testing.T) {
	app := pdbr.New(config.DefaultConfig())
	app.Close()
	app.Close()
}
