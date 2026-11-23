# pdbcore — shared-module extraction plan

> **Status: as built (M0 done, awaiting the `v0.1.0` tag).**
> `github.com/suprbdev/pdbcore` exists at `/personal/pdbcore` with the package
> set below; pdbq and pdbr both consume it (as the tagged `v0.1.0`
> module). This document is the extraction plan it was built from; the
> module's own `README.md` and package docs are the maintained reference, and
> the M0 report notes the small deviations (`inflect.ByColumns`,
> `watch.Watcher.TriggerName`, `cache` error wording, no `ValidateDatabase`).

See also [architecture.md](architecture.md) for how pdbr consumes these
packages.

## Goals

1. **One engine, two transports.** Introspection, catalog cache, DDL watch,
   smart-comment parsing, PostGIS value handling, auth, transaction/RLS
   execution, error classification, config loading and the plugin registry
   are identical concerns in pdbq and pdbr; they should be one code base with
   one test suite.
2. **Cache interoperability.** A `pdbq schema dump` file must load in `pdbr
   serve --schema.cache_path` and vice versa. That requires a single owner of
   `introspect.Catalog` and `CatalogFormatVersion`.
3. **Byte-identical pdbq behaviour.** The extraction is a pure move: pdbq's
   goldens and e2e suite must pass unchanged against pdbcore.
4. **No transport leakage.** pdbcore never imports `gqlparser`, never sees a
   GraphQL AST, an `*http.Request` for anything but auth/CORS/gzip, or a REST
   URL grammar. If a candidate needs any of those, it stays in the product.
5. **Small, orthogonal packages** — importable one at a time (pdbr does not
   want `jwtmint` unless it mints; a third project may want only
   `introspect`).

## Package map

| pdbcore pkg | From pdbq | Moves | Stays in pdbq (GraphQL-only) |
|---|---|---|---|
| `introspect` | `internal/introspect` (`catalog.go`, `introspect.go`, `enumtables.go`, `diff.go`) | `Catalog`, `Table`, `Column`, `Constraint`, `ForeignKey`, `Index`, `Privileges`, `Enum`, `Composite`, `Function`, `FuncArg`, `Introspect`, `Diff`, `Hash`, `@enum` table conversion, `CatalogFormatVersion` (=3; ownership moves here) | — |
| `smarttags` | `internal/smarttags` | `Parse`, `Tags`, `Omits`, `@behavior` mapping | — |
| `inflect` | `internal/inflect` (helpers + chain types) | `UpperCamel`, `LowerCamel`, `Singularize`, `Pluralize`, `EnumValue`, `Kind` (open string type), `Input`, `Next` | pdbq `Kind*` constants + `Default` (PostGraphile-verbose) |
| `cache` | `internal/cache` | gzip+json envelope, `Save/Load/Check`, format-version + hash checks, 256 MiB bomb guard | — |
| `watch` | `internal/watch` | DDL event trigger + `LISTEN/NOTIFY`, poll-hash fallback, `OnChange(*Catalog)` seam | — |
| `postgis` | `internal/postgis` (`postgis.go`, `ops.go`) | flavour detection, GeoJSON/WKT/EWKT parse → single-param constructor, ops table (17), derived accessors (8), transform/simplify/KNN SQL | the GraphQL `GeoJSON` scalar + input types (plugin `plugins/postgis`) |
| `auth` | `internal/server/auth.go`, `jwks.go` | `Authenticator` (jwt/headers/none, HS*/RS*/ES* via JWKS cache, trusted proxies, role resolve/allowlist), own `auth.Config` | — |
| `pgexec` | `internal/exec` (transport-neutral half) + `pdbq.go` `Connect` | pool setup, `Querier`, `RunTx` (isolation, `SET LOCAL ROLE`, `set_config`, retry loop on 40001/40P01), `quoteIdent`, `validRole`, `validClaimKey`, `Classify` | GraphQL `Executor`, `Operation`/`Result` (gqlerror), depth/cost limits over the AST, introspection resolution, `gqlError` |
| `jwtmint` | `internal/exec/mint.go` | composite-typed function result → HS256 JWT | field-level plumbing (`mintField`) |
| `httpx` | `internal/server` (transport-neutral half) | CORS middleware + preflight, gzip (pool, `q=` parsing, skip OPTIONS), `ListenAndServe` scaffolding (ReadHeaderTimeout 10s, Read = request_timeout+10s, Idle 120s, graceful shutdown 10s), `/healthz` handler | GraphQL handler, APQ, GraphiQL, SDL endpoint |
| `confx` | `internal/config` (helpers) | koanf `Load(envPrefix, path, changedFlags)`, env key mapper (prefix param), `ExampleYAML` reflection over `koanf`+`doc` tags, `ParseTrustedProxy`, shared section structs | pdbq `Server` section (graphiql, apq, max_depth, …), pdbq `TX.PerRequest`, product `Config` root + `Validate` |
| `plugin` | `internal/plugin` (generic half) | `Plugin{Name,Priority}`, `Registry` (Add/All/Enabled, stable priority sort), `CatalogHook`, `InflectionHook`, chain helpers (`Inflector`, `TransformCatalog`) | `SchemaHook`, `CompileHook`, `RequestHook` (typed against pdbq's schema/compile/exec) and their `Registry` helpers |
| `smartcomments/catalog` | `internal/plugins/smartcomments` (CatalogHook half) | tag indexing, `@omit create/update/delete` → privilege flags, `@primaryKey`/`@unique`/`@foreignKey` synthesis, `@notNull`/`@nullable`, `@filterable`/`@sortable` synthetic index, `@omit` on FKs/uniques/functions/tables | InflectionHook (GraphQL name rewrite) + SchemaHook (per-action field removal, description stripping) |
| `testutil` | `internal/testutil` + `db/init/01-schema.sql` | `FixtureCatalog()` and `FixtureSQL` (`go:embed`) — one fixture for both products' unit + e2e tests | GraphQL golden documents |

Not moved at all (GraphQL-only, listed for completeness): `internal/schema`,
`internal/compile`, `plugins/simplenames`, `plugins/advancedfilters`,
`plugins/nestedmutations` (AST parts) — see "What NOT to move".

## API sketch per package

### `introspect`

```go
const CatalogFormatVersion = 7 // v0.4.0; 3 at extraction
type Catalog struct { FormatVersion int; Schemas []string; Tables []*Table; Enums []*Enum; Composites []*Composite; Functions []*Function; Ranges []*RangeType; PostGIS *PostGISInfo }
type Column struct { …; TypeSchema, PGType, BaseTypeSchema string; … } // BaseTypeSchema: schema of the resolved base type (domains unwrapped)
type Table struct { Schema, Name string; Kind RelKind; Columns []*Column; PrimaryKey *Constraint; Uniques []*Constraint; ForeignKeys []*ForeignKey; Indexes []*Index; Privileges Privileges; RLSEnabled bool; Comment string }
func Introspect(ctx context.Context, q Querier, schemas []string) (*Catalog, error)
func (c *Catalog) Table(schema, name string) *Table
func (c *Catalog) Hash() (string, error)
func Diff(old, new *Catalog) []string
```

Unchanged from pdbq except the import path. `Querier` is a minimal pgx
interface so callers can pass a pool or a tx.

### `smarttags`

```go
type Tags map[string][]string
func Parse(comment string) (Tags, string) // tags + description with tag lines stripped
func (t Tags) Omits() Omits                // parsed @omit tokens (Read, All, Many, Create, Update, Delete, Filter, Order, Object)
```

### `inflect`

```go
type Kind string             // open: each product defines its constants
type Input struct { Schema, Table, Column string; Columns []string; Constraint, Function, Enum, Value string; IsList bool }
type Next func(kind Kind, in Input) string
func UpperCamel(s string) string; func LowerCamel(s string) string
func Singularize(s string) string; func Pluralize(s string) string
func EnumValue(label string) string
```

The **split**: helpers and chain types are shared; product kinds and
`Default` live in each product (`pdbq/internal/inflect` keeps
`KindAllRowsField` … + verbose `Default`; `pdbr/internal/inflect` defines
`resource_path field_name relation_forward relation_backward rpc_name
computed_field enum_value` + identity `Default`). Because `Kind` is an open
string, `InflectionHook` plugins compile against pdbcore only.

### `cache`

```go
func Save(path string, cat *introspect.Catalog) error
func Load(path string) (*introspect.Catalog, error) // rejects version mismatch, corrupt gzip, > 256 MiB
func Check(path string, live *introspect.Catalog) (diff string, err error)
```

### `watch`

```go
type Watcher struct { Pool *pgxpool.Pool; Schemas []string; Channel string; PollInterval time.Duration; Log *slog.Logger; OnChange func(*introspect.Catalog); TriggerName string; InitialHash string }
// InitialHash (v0.4.0): Catalog.Hash of the boot catalog; Run re-introspects once when listening and only calls OnChange when the hash differs
func (w *Watcher) Run(ctx context.Context) error
```

### `postgis`

```go
type Flavour int // None, Geometry, Geography
func Detect(cat *introspect.Catalog) Flavour
func ParseGeom(v any) (Geom, error)          // GeoJSON object/string, WKT, EWKT → canonical
func ConstructorSQL(param string, srid int) string // one value-independent CASE over ONE $n (v0.4.0):
// CASE WHEN left($n, 1) = '{' THEN [ST_Transform(]ST_GeomFromGeoJSON($n)[, srid)] ELSE ST_GeomFromEWKT($n) END
var Ops map[string]Op                          // 30 predicates: name → SQL template + arity
var Accessors map[string]Accessor              // 25 derived accessors
func TransformSQL(expr string, srid int) string; func SimplifySQL(expr, tolParam string) string; func KNNSQL(col, param string) string
```

### `auth`

```go
type Config struct { Mode string; JWTSecret, JWKSURL string; JWKSCacheTTL time.Duration; JWTIssuer, JWTAudience string; HeaderPrefix string; TrustedProxies []netip.Prefix; RoleClaim, DefaultRole, AnonymousRole string; AllowedRoles []string }
type Authenticator struct{ … }
func New(cfg Config) *Authenticator
func (a *Authenticator) Authenticate(r *http.Request) (claims map[string]any, role string, err error)
var ErrInvalidToken, ErrRoleNotAllowed error // products map to 401 / 403 (pdbr) or GraphQL errors (pdbq)
```

`HeaderPrefix` is a parameter (pdbq passes `X-Pdbq-Claim-`, pdbr
`X-Pdbr-Claim-`).

### `pgexec`

```go
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type PoolConfig struct { URL string; MaxConns int32; ConnectTimeout, StatementTimeout time.Duration }
func Connect(ctx context.Context, cfg PoolConfig) (*pgxpool.Pool, error)

type Identity struct { Role string; Claims map[string]any; ClaimsPrefix string } // ClaimsPrefix "" → "pdbq.claims"
type TxOptions struct { Isolation pgx.TxIsoLevel; MaxRetries int; RLS bool; ScanBeforeCommit bool; Log *slog.Logger }

// RunTx begins a tx, applies SET LOCAL ROLE + set_config claims (RLS true), calls fn,
// commits unless fn returned an error (rollback). On 40001/40P01 the whole thing
// re-runs up to MaxRetries times (no backoff). fn may run more than once.
func RunTx(ctx context.Context, pool *pgxpool.Pool, opts TxOptions, id Identity, fn func(q Querier) error) error

// QueryTx (v0.4.0) sends identity + stmts (+ BEGIN/COMMIT when Isolation is set or
// ScanBeforeCommit) as one pipelined batch: one round trip instead of RunTx's four or more.
type ConnAcquirer interface { Acquire(ctx context.Context) (*pgxpool.Conn, error) }
type Stmt struct { SQL string; Args []any; Scan func(row pgx.Row) error }
func QueryTx(ctx context.Context, db ConnAcquirer, opts TxOptions, id Identity, stmts ...Stmt) error

func QuoteIdent(s string) string
func ValidRole(s string) bool      // [A-Za-z0-9_]+, not none/public/pg_* in any case (v0.4.0: "none" is RESET ROLE)
func ValidateClaimsPrefix(prefix string) error // v0.4.0; confx.ValidateRLS checks both at config load
func ValidClaimKey(s string) bool

type Class int // OK, UserInput (22*, 23502, 23514, 42* other), Permission (42501), Conflict (23505, 23503, 23P01, 40001/40P01 exhausted), Resource (53*, 08*), Timeout (57014), Internal
type Detail int // Dev, Prod, Strict
type Classified struct { Class Class; SQLState string; Message string; PG *pgconn.PgError }
func Classify(err error, detail Detail) Classified
```

`Message` policy (from pdbq's `sanitizePGMessage`): Dev → full PG message;
Prod → `22*`/`23*` pass through, rest generic; Strict → every DB message
generic. Products map `Class` → status (pdbr) or GraphQL error + `code`
extension (pdbq). The status table itself is product code.

### `jwtmint`

```go
type Options struct { Schema, Type, Secret, Issuer, Audience string }
func (o Options) Enabled() bool
func (o Options) MintsFunction(f *introspect.Function) bool // returns the jwt composite (or SETOF it)
func Mint(o Options, raw json.RawMessage, set bool) (json.RawMessage, error)
```

### `httpx`

```go
func CORS(origins []string, next http.Handler) http.Handler   // exact allowlist or "*", preflight
func Gzip(next http.Handler) http.Handler                        // sync.Pool writers, q= parsing, skips OPTIONS
func Healthz(w http.ResponseWriter, r *http.Request)             // {"status":"ok"}
type ServeOptions struct { Addr string; RequestTimeout time.Duration; Log *slog.Logger }
func ListenAndServe(ctx context.Context, h http.Handler, o ServeOptions) error // ReadHeader 10s, Read = timeout+10s, Idle 120s, graceful 10s
```

### `confx`

```go
func Load(envPrefix string, path string, flags *pflag.FlagSet, dst any) error // defaults already in dst; YAML < env < changed flags
func EnvKeyMapper(prefix string) func(string) string             // strip prefix, lower, "__"→"_", "_"→"."
func ExampleYAML(v any) string                                   // reflect over koanf+doc tags
func ParseTrustedProxy(s string) (netip.Prefix, error)
func Discover(names ...string) string                             // first existing of pdbX.yaml/.yml in cwd

// shared section structs (koanf + doc tags), embedded by each product's Config:
type Database struct { URL string; MaxConns int32; ConnectTimeout, StatementTimeout time.Duration }
type RLS struct { Enabled bool; DefaultRole, AnonymousRole, RoleClaim string; AllowedRoles []string; ClaimsPrefix string; Auth Auth }
type Auth struct { Mode, JWTSecret, JWKSURL string; JWKSCacheTTL time.Duration; JWTIssuer, JWTAudience, HeaderPrefix string; TrustedProxies []string; JWTType string }
type TX struct { Mutations bool; Isolation string; MaxRetries int }   // NO PerRequest — pdbq adds it in its own TX wrapper
type Watch struct { Enabled bool; PollInterval time.Duration; Channel string }
type Errors struct { Detail string }
type Plugins struct { Disabled []string; Settings map[string]map[string]any }
type Log struct { Level, Format string }
type Schema struct { Schemas []string; CachePath string; Functions bool }
type Filters struct { IndexedOnly bool; AllowColumns map[string][]string }
```

Config split: each product owns `Config{Database confx.Database; Server
<product>Server; Schema confx.Schema; …}`, its `DefaultConfig()` (product
defaults such as `watch.channel` `pdbq_ddl` vs `pdbr_ddl`, `header_prefix`),
and its `Validate()`. Shared validation helpers (`ValidateDatabase`,
`ValidateRLS`, `ValidateWatch`, …) live in confx so the 16 pdbq checks are
not duplicated.

Decided (M0): koanf's `squash` embedding works for loading but cannot
interleave the embedded block's field order in the generated example YAML,
so pdbq keeps its own four-field `TX` struct (with `per_request`) and calls
`confx.ValidateTX(confx.TX{…})`; pdbr uses `confx.TX` directly (no
`per_request`).

### `plugin`

```go
type Plugin interface { Name() string; Priority() int }
type CatalogHook interface { TransformCatalog(ctx context.Context, c *introspect.Catalog) error }
type InflectionHook interface { Inflect(kind inflect.Kind, in inflect.Input, next inflect.Next) string }
type Registry struct{ … }
func NewRegistry(ps ...Plugin) *Registry
func (r *Registry) Add(p Plugin); All() []Plugin; Enabled(disabled map[string]bool) []Plugin
func (r *Registry) TransformCatalog(ctx, disabled, cat) error
func (r *Registry) Inflector(disabled map[string]bool, base inflect.Next) inflect.Next // base = product Default
```

Registry split: the generic registry lives in pdbcore; each product wraps it
(`pdbq/internal/plugin.Registry` embeds `*pdbcore/plugin.Registry` and adds
`TransformSchema` / `CompileChain` / `RequestHooks`; pdbr adds
`TransformAPI` / `CompileChain` / `RequestHooks`). Product hook interfaces
stay product-typed. `Inflector` gains an explicit `base` argument because
`Default` is no longer in the shared package.

### `smartcomments/catalog`

```go
func New() *Hook // implements plugin.CatalogHook + plugin.Plugin (name "smart-comments", priority 50)
func Index(cat *introspect.Catalog) *Index // parsed tags per object, reused by product halves
```

Each product's `plugins/smartcomments` embeds the core hook and adds its
naming and surface halves.

### `testutil`

```go
func FixtureCatalog() *introspect.Catalog
//go:embed fixture.sql
var FixtureSQL string
```

Unification: `pdbq/db/init/01-schema.sql` becomes `pdbcore/testutil/fixture.sql`
and is embedded; both products' compose/CI stacks load it from the module
(`go run ./cmd/… fixture` or a tiny `pdbcore/testutil/cmd/fixture` printer),
so there is one file. **Drift fixes to make during extraction:**

- `metrics` view exists in `FixtureCatalog()` but is missing from the SQL →
  add `CREATE VIEW metrics …` to the SQL.
- RLS flag: the Go fixture marks `users` RLS-enabled while the SQL enables RLS
  on `posts` → align both (SQL is authoritative: `posts`).
- Functions `unpublish_post`, `word_lengths`, `authenticate` and the `jwt`
  composite type exist in SQL but not in `FixtureCatalog()` → add them.
- `@costMultiplier` FK comment present in SQL but absent from the Go fixture →
  add the comment to the fixture FK.
- Add a test in pdbcore that loads `FixtureSQL` into a DB (skipped without
  `PDBCORE_TEST_DATABASE_URL`), introspects it and diffs against
  `FixtureCatalog()` — the permanent drift gate.

## Versioning & release policy

- pdbcore uses semver tags `v0.x.y` while pre-1.0; minor bumps may break API,
  patch bumps may not. Both products pin an exact version in `go.mod`.
- `introspect.CatalogFormatVersion` is owned by pdbcore. Bumping it is a
  pdbcore minor release and **both** products must upgrade in lockstep,
  because a cache written by one product must load in the other. The cache
  envelope records the format version; loaders refuse mismatches (existing
  behaviour).
- Cache files carry only the catalog (never GraphQL SDL or OpenAPI), so
  pdbq↔pdbr interoperability holds by construction (pdbr invariant 6).
- Fixture changes (`testutil`) are pdbcore patch/minor releases; each product
  regenerates goldens when it upgrades.
- Release flow follows go-release-template: tag → GitHub release with notes;
  no binaries (library only).

## pdbq migration checklist

- [ ] Create `/personal/pdbcore` (`go mod init github.com/suprbdev/pdbcore`, Go 1.26, MIT, `.golangci.yaml`, CI `lint-test`).
- [ ] Branch `pdbcore-extract` in pdbq.
- [ ] Move packages per the map (`git mv` + import rewrite); keep GraphQL halves in pdbq (`exec`, `server`, `plugin`, `smartcomments`, `config`, `inflect` split as described).
- [ ] `go.mod` `replace github.com/suprbdev/pdbcore => ../pdbcore` during development.
- [ ] `pdbq/internal/inflect`: keep `Kind*` + `Default`; re-export helpers from pdbcore or import directly.
- [ ] `pdbq/internal/config`: embed confx sections; keep `Server`, `TX.PerRequest`, root `Config`, `Validate`; `Load` → `confx.Load("PDBQ_", …)`; `ExampleYAML` → `confx.ExampleYAML(cfg)`; **`make example-config` output must be byte-identical.**
- [ ] `pdbq/internal/exec`: replace `runTx`/`applyRLS`/`quoteIdent`/`validRole`/`sanitizePGMessage` with `pgexec`; `Executor`, limits, introspection, `gqlError` stay.
- [ ] `pdbq/internal/server`: use `httpx` CORS/gzip/listen/healthz and `auth.Authenticator`.
- [ ] `pdbq/internal/plugin`: embed `pdbcore/plugin.Registry`; `Inflector(disabled, inflect.Default)`.
- [ ] `pdbq/internal/testutil` → import `pdbcore/testutil`; `db/init/01-schema.sql` regenerated from `FixtureSQL` (or compose mounts it via a generated file); apply drift fixes; **regenerate goldens and confirm the diff is exactly the fixture-drift delta** (metrics view, functions), nothing else.
- [ ] `make test` green; `make test-e2e` green; `make fuzz` 30s green.
- [ ] Goldens byte-identical to `main` except for the documented fixture-drift additions.
- [ ] Tag `pdbcore v0.1.0`.
- [ ] Drop the `replace`, `go get github.com/suprbdev/pdbcore@v0.1.0`, `go mod tidy`.
- [ ] Update pdbq `CLAUDE.md` (architecture paths), `docs/plugins.md` (import paths for hook interfaces).
- [ ] Tag `pdbq v0.11.0`.

## Risks

| Risk | Mitigation |
|---|---|
| Import cycles: `pgexec` must not import `plugin`; `plugin` must not import `exec` — this is exactly the cycle behind pdbq's duplicated `exec.RequestHook` | pdbcore/plugin only knows `introspect` + `inflect`. Product-typed hooks (`RequestHook`, `CompileHook`) live in the product's `plugin` package, which may import the product's `exec`; the product `exec` never imports `plugin` (it takes a `[]RequestHook`-shaped interface declared locally). Once hooks are generic over an `Operation` interface the structural duplicate can disappear; until then it stays, documented, in the product. |
| Behaviour drift during the move (a "small cleanup" changes SQL or config output) | Goldens byte-identical gate; `config example` diff gate; e2e green; the extraction PR is review-only for moves — refactors go in follow-ups. |
| Fixture drift fixes change goldens | Land the drift fixes as a separate, first commit in the extraction branch so their golden delta is reviewable on its own. |
| koanf struct embedding for shared config sections (`squash` behaviour, `doc` tag reflection over embedded structs) | Spike in confx first; `ExampleYAML` test asserts pdbq's example file reproduces byte-for-byte. |
| pdbcore API churn while pdbr is being built | pdbr develops against a `replace` too; both products upgrade at pdbcore minor tags only. |
| Two repos to release for one bug fix | Accept for now; a `go.work` file in a parent dir eases local development. |

## What NOT to move (and why)

- `pdbq/internal/schema` — GraphQL type system, `ast.Schema`, SDL. pdbr has
  `api` instead; there is no shared abstraction worth the coupling.
- `pdbq/internal/compile` — consumes `*ast.Field`; the REST compiler consumes
  `plan.Request`. Shared *SQL rendering helpers* (`quoteIdent`, table refs,
  keyset predicate builder, chunked `json_build_object`) could move later
  into a `pdbcore/sqlgen` package once both compilers exist and the shape is
  proven identical — deliberately **not** in v0.1.0.
- GraphQL half of `exec` (`Executor`, depth/cost over AST, introspection
  root fields, `gqlerror` mapping) — transport-specific.
- `server` GraphQL handler, APQ, GraphiQL, SDL endpoint — no REST equivalent.
- `plugins/simplenames` — GraphQL naming only; pdbr uses raw identifiers.
- `plugins/advancedfilters`, `plugins/nestedmutations`, `plugins/postgis`
  SchemaHook/CompileHook halves — typed against pdbq's schema/compile; pdbr
  writes its own halves against `api`/`plan`. Only their catalog/inflection
  logic (already in `smartcomments/catalog` and `postgis`) is shared.
- `pdbq.go` / `facade.go` — product embedding surfaces.
- Golden test data — transport-specific inputs.
