package compile_test

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/inflect"
	"github.com/suprbdev/pdbr/internal/parse"
	"github.com/suprbdev/pdbr/internal/plugins/nestedmutations"
	postgisplugin "github.com/suprbdev/pdbr/internal/plugins/postgis"
)

var update = flag.Bool("update", false, "rewrite golden files")

// goldenBasePath is the base path the golden request lines are written for.
const goldenBasePath = "/api"

// goldenParseOptions mirrors server.ParseOptions with every plugin gate open.
var goldenParseOptions = parse.Options{
	MaxEmbedDepth: 5, MaxPageSize: 100, Etags: true, CSV: true,
	RelationFilters: true, ComputedFilters: true, RelationOrder: true, ComputedOrder: true, Spatial: true,
	NestedKeys: nestedmutations.NestedKeys,
}

// goldenCompile is the compile chain the goldens run through: the core
// compiler wrapped by the nested-mutations CompileHook (the only built-in
// that emits SQL of its own; nested_* goldens exercise it).
func goldenCompile(c *compile.Compiler) compile.Func {
	return nestedmutations.New(nil).WrapCompile(c.Compile)
}

// goldenCatalog is the shared fixture plus golden-only tables the fixture
// lacks: a composite-primary-key table (memberships), a table in a second
// schema (audit.log), the PostgreSQL 18 temporal tables (temporalTables),
// and the golden-only functions in
// goldenOnlyRPCs. They exist only in memory; the live fixture database does
// not have them.
func goldenCatalog() *introspect.Catalog {
	cat := testutil.FixtureCatalog()
	cat.Schemas = []string{"public", "audit"}
	priv := introspect.Privileges{Select: true, Insert: true, Update: true, Delete: true}
	cat.Tables = append(cat.Tables,
		&introspect.Table{
			Schema: "public", Name: "memberships", Kind: introspect.RelTable,
			Columns: []*introspect.Column{
				{Name: "user_id", Position: 1, PGType: "int4", NotNull: true},
				{Name: "team", Position: 2, PGType: "text", NotNull: true},
				{Name: "role", Position: 3, PGType: "text"},
			},
			PrimaryKey:  &introspect.Constraint{Name: "memberships_pkey", Columns: []string{"user_id", "team"}},
			ForeignKeys: []*introspect.ForeignKey{{Name: "memberships_user_id_fkey", Columns: []string{"user_id"}, RefSchema: "public", RefTable: "users", RefColumns: []string{"id"}}},
			Privileges:  priv,
		},
		&introspect.Table{
			Schema: "audit", Name: "log", Kind: introspect.RelTable,
			Columns: []*introspect.Column{
				{Name: "id", Position: 1, PGType: "int8", NotNull: true, Generated: true},
				{Name: "action", Position: 2, PGType: "text", NotNull: true},
				{Name: "at", Position: 3, PGType: "timestamptz", NotNull: true, HasDefault: true},
			},
			PrimaryKey: &introspect.Constraint{Name: "log_pkey", Columns: []string{"id"}},
			Indexes:    []*introspect.Index{{Name: "log_action_idx", Columns: []string{"action"}, Method: "btree"}},
			Privileges: priv,
		},
	)
	cat.Tables = append(cat.Tables, temporalTables(priv)...)
	// A void function (the fixture has none) so the void shape has a golden,
	// plus defaulted and variadic arguments.
	pg := "pg_catalog"
	cat.Functions = append(cat.Functions, &introspect.Function{
		Schema: "public", Name: "noop", ReturnType: "void", ReturnTypeSchema: pg, Volatility: introspect.VolatilityVolatile,
	}, &introspect.Function{
		Schema: "public", Name: "greet", ReturnType: "text", ReturnTypeSchema: pg, Volatility: introspect.VolatilityImmutable,
		Args: []introspect.FuncArg{
			{Name: "name", PGType: "text", TypeSchema: pg, Mode: introspect.ArgModeIn, HasDefault: true, Default: "'world'::text"},
			{Name: "greeting", PGType: "text", TypeSchema: pg, Mode: introspect.ArgModeIn, HasDefault: true, Default: "'hello'::text"},
		},
	}, &introspect.Function{
		Schema: "public", Name: "mood_counts", ReturnType: "record", ReturnTypeSchema: pg, ReturnsSet: true, Volatility: introspect.VolatilityStable,
		ReturnColumns: []introspect.FuncArg{
			{Name: "mood", PGType: "mood", TypeSchema: "public", Mode: introspect.ArgModeTable},
			{Name: "n", PGType: "int8", TypeSchema: pg, Mode: introspect.ArgModeTable},
		},
	}, &introspect.Function{
		Schema: "public", Name: "user_stats", ReturnType: "record", ReturnTypeSchema: pg, Volatility: introspect.VolatilityStable,
		Args: []introspect.FuncArg{{Name: "uid", PGType: "int4", TypeSchema: pg, Mode: introspect.ArgModeIn}},
		ReturnColumns: []introspect.FuncArg{
			{Name: "posts", PGType: "int8", TypeSchema: pg, Mode: introspect.ArgModeOut},
			{Name: "last_title", PGType: "text", TypeSchema: pg, Mode: introspect.ArgModeOut},
		},
	}, &introspect.Function{
		Schema: "public", Name: "sum_all", ReturnType: "int4", ReturnTypeSchema: pg, Volatility: introspect.VolatilityImmutable,
		Args: []introspect.FuncArg{
			{Name: "base", PGType: "int4", TypeSchema: pg, Mode: introspect.ArgModeIn},
			{Name: "nums", PGType: "_int4", TypeSchema: pg, Mode: introspect.ArgModeVariadic},
		},
	})
	return cat
}

// temporalTables are the PostgreSQL 18 key variants (tk_ prefix, the same
// shapes test/e2e_temporal_test.go creates on PG18+): tk_bookings has only a
// temporal primary key (room_id, valid_at WITHOUT OVERLAPS), tk_rates a
// plain primary key plus a temporal unique, tk_notes a PERIOD foreign key to
// tk_bookings, tk_readings a NOT ENFORCED foreign key to tk_rooms.
func temporalTables(priv introspect.Privileges) []*introspect.Table {
	pg := "pg_catalog"
	col := func(pos int, name, typ string) *introspect.Column {
		return &introspect.Column{Name: name, Position: pos, PGType: typ, TypeSchema: pg, NotNull: true}
	}
	ident := col(1, "id", "int4")
	ident.HasDefault, ident.Generated, ident.GeneratedKind = true, true, introspect.GeneratedIdentity
	return []*introspect.Table{
		{
			Schema: "public", Name: "tk_rooms", Kind: introspect.RelTable,
			Columns:    []*introspect.Column{col(1, "id", "int4"), col(2, "name", "text")},
			PrimaryKey: &introspect.Constraint{Name: "tk_rooms_pkey", Columns: []string{"id"}},
			Privileges: priv,
		},
		{
			Schema: "public", Name: "tk_bookings", Kind: introspect.RelTable,
			Columns:     []*introspect.Column{col(1, "room_id", "int4"), col(2, "valid_at", "daterange"), col(3, "guest", "text")},
			PrimaryKey:  &introspect.Constraint{Name: "tk_bookings_pkey", Columns: []string{"room_id", "valid_at"}, WithoutOverlaps: true},
			ForeignKeys: []*introspect.ForeignKey{{Name: "tk_bookings_room_id_fkey", Columns: []string{"room_id"}, RefSchema: "public", RefTable: "tk_rooms", RefColumns: []string{"id"}}},
			Indexes:     []*introspect.Index{{Name: "tk_bookings_guest_idx", Columns: []string{"guest"}, Method: "btree"}},
			Privileges:  priv,
		},
		{
			Schema: "public", Name: "tk_rates", Kind: introspect.RelTable,
			Columns:    []*introspect.Column{col(1, "id", "int4"), col(2, "room_id", "int4"), col(3, "valid_at", "daterange"), col(4, "price", "numeric")},
			PrimaryKey: &introspect.Constraint{Name: "tk_rates_pkey", Columns: []string{"id"}},
			Uniques:    []*introspect.Constraint{{Name: "tk_rates_room_id_valid_at_key", Columns: []string{"room_id", "valid_at"}, WithoutOverlaps: true}},
			Privileges: priv,
		},
		{
			Schema: "public", Name: "tk_notes", Kind: introspect.RelTable,
			Columns:    []*introspect.Column{ident, col(2, "room_id", "int4"), col(3, "valid_at", "daterange"), col(4, "note", "text")},
			PrimaryKey: &introspect.Constraint{Name: "tk_notes_pkey", Columns: []string{"id"}},
			ForeignKeys: []*introspect.ForeignKey{{
				Name: "tk_notes_room_id_valid_at_fkey", Columns: []string{"room_id", "valid_at"},
				RefSchema: "public", RefTable: "tk_bookings", RefColumns: []string{"room_id", "valid_at"}, Period: true,
			}},
			Privileges: priv,
		},
		{
			Schema: "public", Name: "tk_readings", Kind: introspect.RelTable,
			Columns:     []*introspect.Column{col(1, "id", "int4"), col(2, "room_id", "int4"), col(3, "celsius", "numeric")},
			PrimaryKey:  &introspect.Constraint{Name: "tk_readings_pkey", Columns: []string{"id"}},
			ForeignKeys: []*introspect.ForeignKey{{Name: "tk_readings_room_id_fkey", Columns: []string{"room_id"}, RefSchema: "public", RefTable: "tk_rooms", RefColumns: []string{"id"}, NotEnforced: true}},
			Privileges:  priv,
		},
	}
}

func goldenBuilt(t testing.TB) *api.Built {
	t.Helper()
	b := api.New(goldenCatalog(), inflect.Default, api.Options{
		FilterIndexedOnly: true,
		Functions:         true,
		BasePath:          goldenBasePath,
		Schemas:           []string{"public", "audit"},
		MaxPageSize:       100,
		Etags:             true,
		JWTType:           "public.jwt",
		Version:           "test",
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	// The postgis plugin registers the <col>_<accessor> pseudo-columns the
	// spatial_* goldens select; the parse gates themselves come from
	// goldenParseOptions.
	if err := postgisplugin.New(nil).TransformAPI(context.Background(), b); err != nil {
		t.Fatalf("postgis plugin: %v", err)
	}
	if err := nestedmutations.New(nil).TransformAPI(context.Background(), b); err != nil {
		t.Fatalf("nested-mutations plugin: %v", err)
	}
	built, err := b.Build()
	if err != nil {
		t.Fatalf("build api: %v", err)
	}
	return built
}

// readHTTPFile parses a testdata/*.http request: first line `METHOD
// /path?query`, then optional `Header: value` lines, then a blank line and
// an optional body.
func readHTTPFile(t *testing.T, path string) *http.Request {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		t.Fatalf("%s: empty", path)
	}
	method, target, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
	if !ok || !strings.HasPrefix(target, "/") {
		t.Fatalf("%s: first line must be `METHOD /path`, got %q", path, sc.Text())
	}
	headers := http.Header{}
	var body strings.Builder
	inBody := false
	for sc.Scan() {
		line := sc.Text()
		if inBody {
			body.WriteString(line)
			body.WriteString("\n")
			continue
		}
		if strings.TrimSpace(line) == "" {
			inBody = true
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("%s: bad header line %q", path, line)
		}
		headers.Add(strings.TrimSpace(k), strings.TrimSpace(v))
	}
	var rd io.Reader
	if b := strings.TrimSpace(body.String()); b != "" {
		rd = strings.NewReader(b)
		if headers.Get("Content-Type") == "" {
			headers.Set("Content-Type", "application/json")
		}
	}
	r := httptest.NewRequest(method, target, rd)
	for k, vs := range headers {
		r.Header[k] = vs
	}
	return r
}

// renderRequest runs route matching, parsing and compilation for r exactly
// as the server does and renders the golden text; failures render as a
// `-- error:` line so grammar errors can be goldens too.
func renderRequest(t *testing.T, built *api.Built, c *compile.Compiler, r *http.Request) string {
	t.Helper()
	target := r.URL.RequestURI()
	fail := func(status int, code, detail string) string {
		return fmt.Sprintf("-- request: %s %s\n-- error: %d %s: %s\n", r.Method, target, status, code, detail)
	}
	m, ok := built.Match(strings.TrimPrefix(r.URL.EscapedPath(), goldenBasePath))
	if !ok {
		return fail(http.StatusNotFound, "not_found", "no such route")
	}
	if !m.Route.Allows(api.Method(r.Method)) {
		return fail(http.StatusMethodNotAllowed, "method_not_allowed", "Allow: "+m.Route.AllowHeader())
	}
	p, err := parse.Parse(built, m, r, goldenParseOptions)
	if err != nil {
		return failFrom(t, fail, err)
	}
	stmt, err := goldenCompile(c)(context.Background(), &compile.Request{Plan: p, Built: built, MaxEmbedDepth: 5, MaxPageSize: 100})
	if err != nil {
		return failFrom(t, fail, err)
	}
	return compile.RenderGolden(r.Method, target, stmt)
}

func failFrom(t *testing.T, fail func(int, string, string) string, err error) string {
	t.Helper()
	var ue *compile.UserError
	if !errors.As(err, &ue) {
		t.Fatalf("non-UserError failure: %v", err)
	}
	return fail(ue.Status, ue.Code, ue.Detail)
}

// TestGolden compiles each testdata/*.http request through match -> parse ->
// compile and compares the rendered statement against the .sql file next to
// it (the same format `pdbr request --print-sql` prints). Run with -update
// to regenerate; review the diff — a changed golden is a behaviour change.
func TestGolden(t *testing.T) {
	built := goldenBuilt(t)
	c := compile.New(built)
	files, err := filepath.Glob(filepath.Join("testdata", "*.http"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no golden inputs found: %v", err)
	}
	for _, file := range files {
		name := strings.TrimSuffix(filepath.Base(file), ".http")
		t.Run(name, func(t *testing.T) {
			got := renderRequest(t, built, c, readHTTPFile(t, file))
			golden := strings.TrimSuffix(file, ".http") + ".sql"
			if *update {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run with -update to create)", err)
			}
			if string(want) != got {
				t.Errorf("golden mismatch for %s\n--- want\n%s\n--- got\n%s", name, want, got)
			}
		})
	}
}

// goldenOnlyRPCs are the functions goldenCatalog adds to the fixture.
var goldenOnlyRPCs = map[string]bool{"noop": true, "greet": true, "sum_all": true, "mood_counts": true, "user_stats": true}

// TestGoldenAgainstDatabase executes every golden statement against the
// live fixture database (PDBR_TEST_DATABASE_URL; skipped otherwise), each in
// a transaction that is rolled back: a golden that does not run is a bug.
// Goldens over the golden-only tables (memberships, audit.log, the tk_
// temporal tables) are skipped — they do not exist in the fixture.
func TestGoldenAgainstDatabase(t *testing.T) {
	url := os.Getenv("PDBR_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)
	built := goldenBuilt(t)
	c := compile.New(built)
	files, _ := filepath.Glob(filepath.Join("testdata", "*.http"))
	for _, file := range files {
		name := strings.TrimSuffix(filepath.Base(file), ".http")
		t.Run(name, func(t *testing.T) {
			r := readHTTPFile(t, file)
			if strings.Contains(r.URL.Path, "/memberships") || strings.Contains(r.URL.Path, "/tk_") || strings.HasPrefix(r.URL.Path, "/api/audit/") || goldenOnlyRPCs[path.Base(r.URL.Path)] && strings.Contains(r.URL.Path, "/rpc/") {
				t.Skip("golden-only table")
			}
			m, ok := built.Match(strings.TrimPrefix(r.URL.EscapedPath(), goldenBasePath))
			if !ok || !m.Route.Allows(api.Method(r.Method)) {
				t.Skip("routing golden")
			}
			p, err := parse.Parse(built, m, r, goldenParseOptions)
			if err != nil {
				t.Skip("error golden")
			}
			stmt, err := goldenCompile(c)(ctx, &compile.Request{Plan: p, Built: built, MaxEmbedDepth: 5, MaxPageSize: 100})
			if err != nil {
				t.Skip("error golden")
			}
			// Every golden runs inside a rolled-back transaction so mutation
			// goldens leave the fixture untouched.
			tx, err := conn.Begin(ctx)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			defer tx.Rollback(ctx)
			rows, err := tx.Query(ctx, stmt.SQL, stmt.Args...)
			if err != nil {
				t.Fatalf("query: %v\n%s\n%v", err, stmt.SQL, stmt.Args)
			}
			n := 0
			for rows.Next() {
				n++
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				t.Fatalf("rows: %v\n%s", err, stmt.SQL)
			}
			if stmt.Shape == compile.ShapePage && n != 1 {
				t.Fatalf("page statement returned %d rows", n)
			}
		})
	}
}

// TestUserTextNeverRewritesSQL pins two robustness rules: response JSON keys
// are opaque to the compiler's post-processing (no placeholder collision), and
// the narrow RETURNING list cannot be widened by an alias that looks like a
// column reference.
func TestUserTextNeverRewritesSQL(t *testing.T) {
	built := goldenBuilt(t)
	c := compile.New(built)

	out := renderRequest(t, built, c, httptest.NewRequest("GET", "/api/users?select=id,posts(x%25%25ALIAS%25%25y:title)", nil))
	if !strings.Contains(out, "'x%%ALIAS%%y'") {
		t.Fatalf("embed alias was rewritten:\n%s", out)
	}

	r := httptest.NewRequest("PATCH", `/api/users/1?select=t0."balance":email`, strings.NewReader(`{"full_name":"z"}`))
	r.Header.Set("Content-Type", "application/json")
	out = renderRequest(t, built, c, r)
	if strings.Contains(out, `"balance"`) && !strings.Contains(out, "-- error:") {
		if strings.Contains(out, `RETURNING "email", "balance"`) || strings.Contains(out, `RETURNING "balance"`) {
			t.Fatalf("select alias widened RETURNING:\n%s", out)
		}
	}
	if strings.Contains(out, "-- error:") {
		return // an alias containing quotes may simply be rejected by the grammar; either is fine
	}
	if !strings.Contains(out, `RETURNING "email"`) {
		t.Fatalf("unexpected RETURNING:\n%s", out)
	}
}
