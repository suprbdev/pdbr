# Plugin author guide

pdbr plugins are **compile-time**: a plugin is any Go type implementing
one or more hook interfaces, registered in an ordered registry. Third parties
embed pdbr as a library:

```go
app := pdbr.New(cfg, pdbr.WithPlugins(myPlugin{}), pdbr.WithLogger(logger))
app.Serve(ctx)

// or mount inside an existing mux:
h, err := app.Handler(ctx)   // introspect → plugins → api.Built → http.Handler
mux.Handle("/api/", h)
```

(Out-of-process plugins — go-plugin/wasm — are a possible future direction; these interfaces
are the contract either way.)

The generic half of the system lives in the shared `pdbcore` module and is
identical for pdbq and pdbr; the REST-specific hooks live in pdbr.

| Piece | Package | Notes |
|---|---|---|
| `Plugin` (Name/Priority), `Registry` (Add/All/Enabled), `CatalogHook`, `InflectionHook`, chain helpers `Inflector`, `TransformCatalog` | `pdbcore/plugin` | shared with pdbq — a catalog or naming plugin compiles against both products |
| `inflect.Kind`, `inflect.Input`, `inflect.Next` and the string helpers | `pdbcore/inflect` | kinds are open strings; pdbr's constants live in `internal/inflect` and are re-exported as `pdbr.KindResourcePath`, `pdbr.KindFieldName`, … |
| `APIHook`, `CompileHook`, `RequestHook`, registry chain helpers for them | `pdbr/internal/plugin` | exported through aliases in the root `pdbr` package |
| `api.Builder`, `compile.Func`/`Request`/`Statement`, `exec.Operation`/`Result` | `pdbr/internal/{api,compile,exec}` | aliased as `pdbr.APIBuilder`, `pdbr.CompileFunc`, `pdbr.CompileRequest`, `pdbr.Statement`, `pdbr.Operation`, `pdbr.Result` |

## Hook surfaces

Implement only what you need; every hook is optional:

```go
type Plugin interface {            // required base (pdbcore/plugin)
    Name() string                  // config key under plugins.settings
    Priority() int                 // lower runs earlier / outermost
}

type CatalogHook interface {       // mutate/filter the introspected catalog (pdbcore/plugin)
    TransformCatalog(ctx context.Context, c *introspect.Catalog) error
}
type InflectionHook interface {    // control every generated name (pdbcore/plugin)
    Inflect(kind inflect.Kind, in inflect.Input, next inflect.Next) string
}
type APIHook interface {           // add/remove routes, methods, params, fields, schemas
    TransformAPI(ctx context.Context, b *api.Builder) error
}
type CompileHook interface {       // wrap SQL generation per request, middleware-style
    WrapCompile(next compile.Func) compile.Func
}
type RequestHook interface {       // request lifecycle (auth, logging, tx policy)
    BeforeRequest(ctx context.Context, op *exec.Operation) (context.Context, error)
    AfterRequest(ctx context.Context, op *exec.Operation, res *exec.Result)
}
```

The types the hooks exchange:

```go
// compile: plan → ONE statement
type Func func(ctx context.Context, req *compile.Request) (*compile.Statement, error)

type Request struct {
    Plan          *plan.Request // parsed URL grammar + body: selections, embeds, filters, order, page, mutation
    Built         *api.Built    // resource model + lookup tables the compiler consults
    MaxEmbedDepth int           // server.max_embed_depth
    MaxPageSize   int           // server.max_page_size (default and cap for limit)
}

type Statement struct {
    SQL      string
    Args     []any
    Mutation bool               // drives tx policy and read_only rejection
}

// exec: one HTTP request = one Operation
type Operation struct {
    Method   string             // GET, HEAD, POST, PATCH, PUT, DELETE
    Path     string             // request path below base_path, e.g. /posts/7
    Plan     *plan.Request
    Resource *api.Resource      // the table/view addressed, nil for RPC
    RPC      *api.RPC           // the function addressed, nil for resources
    Claims   map[string]any     // verified request claims exposed to RLS policies
    Role     string             // database role this request runs as ("" = no switch)
    Mutation bool               // write semantics (tx, read_only, retries)
    ForceTx  bool               // force a transaction even when transactions.mutations is off
}
```

`Operation.Claims` and `.Role` are set by the authenticator before
`BeforeRequest` runs; a hook may replace them (e.g. mint claims from an
API key header) or return an error to reject the request. `ForceTx` is the
only field a hook is expected to set for the executor's benefit.

Inside a request, the executor attaches the in-flight operation to the
context before hooks and compilation run, so a `CompileHook` (whose
`compile.Func` only receives a context and the request) can read the
verified claims, role, and operation metadata:
`exec.OperationFromContext(ctx)` / `exec.ClaimsFromContext(ctx)`.

As built, `exec.Result` is `{Status int, Headers http.Header, Data
json.RawMessage, Err error}` (`internal/exec/exec.go`; `Err` is an
`*exec.Error{Status, Code, Detail, PG, Allow}`): `Data` is the client JSON
already unwrapped from the json `data` column (nil for 204/304/HEAD or on
error) — enough for logging and metrics; hooks observe but cannot rewrite it.

### Ordering

Hooks compose middleware-style: call `next(...)` to continue the chain (the
innermost link is pdbr's default behaviour). Ordering is deterministic —
priority ascending, then registration order (built-ins first, then
`WithPlugins` in argument order). Concretely:

- `CatalogHook`s and `APIHook`s run **in order**, each seeing the previous
  one's edits.
- `InflectionHook`s and `CompileHook`s are wrapped **inside-out**, so the
  lowest priority is outermost and sees the request first / decides last.
- `RequestHook.BeforeRequest` runs in order (the returned context threads
  through); `AfterRequest` runs in the same order.

Users can disable any plugin by name (`plugins.disabled: [name]`) and
configure it under `plugins.settings.<name>` — a free-form map handed to the
plugin's constructor (`cfg.Plugins.Settings["my-plugin"]`); pdbr does not
validate its contents.

## Worked example: `path-prefix` + request logging

A ~40-line plugin that puts every resource under a version prefix
(`/api/v1_users`) and logs each request with its resolved role — one
`InflectionHook`, one `RequestHook`, no core changes:

```go
package prefix

import (
    "context"
    "log/slog"
    "time"

    "github.com/suprbdev/pdbcore/inflect"
    "github.com/suprbdev/pdbr"
)

type Plugin struct {
    Prefix string
    Log    *slog.Logger
}

func (p *Plugin) Name() string  { return "path-prefix" }
func (p *Plugin) Priority() int { return 300 } // after every built-in

// Inflect renames the path segment; every other kind (field_name,
// relation_forward, relation_backward, rpc_name, computed_field, enum_value)
// falls through to the default raw-identifier inflector.
func (p *Plugin) Inflect(kind inflect.Kind, in inflect.Input, next inflect.Next) string {
    if kind == pdbr.KindResourcePath {
        return p.Prefix + next(kind, in)
    }
    return next(kind, in)
}

type startKey struct{}

func (p *Plugin) BeforeRequest(ctx context.Context, op *pdbr.Operation) (context.Context, error) {
    return context.WithValue(ctx, startKey{}, time.Now()), nil
}

func (p *Plugin) AfterRequest(ctx context.Context, op *pdbr.Operation, res *pdbr.Result) {
    start, _ := ctx.Value(startKey{}).(time.Time)
    p.Log.Info("request",
        "method", op.Method, "path", op.Path, "role", op.Role,
        "mutation", op.Mutation, "status", res.Status,
        "took", time.Since(start))
}
```

```go
app := pdbr.New(cfg, pdbr.WithPlugins(&prefix.Plugin{Prefix: "v1_", Log: logger}))
```

Because relation keys are derived from `relation_forward` /
`relation_backward` — not from `resource_path` — embeds keep their table-name
keys (`/api/v1_posts?select=*,users(name)`); the OpenAPI document follows the
renamed paths automatically since it is generated from `api.Built`.

`pdbr plugins list` then shows:

```console
$ pdbr plugins list
smart-comments       priority=50   enabled  hooks: catalog, inflection, api
advanced-filters     priority=150  enabled  hooks: api
postgis              priority=160  enabled  hooks: api
nested-mutations     priority=200  enabled  hooks: api, compile, request
path-prefix          priority=300  enabled  hooks: inflection, request
```

(`disabled` replaces `enabled` for names in `plugins.disabled`; a plugin
implementing no hook prints `hooks: none`. The format is pdbq's, with `api`
in place of `schema`.)

## Inflection kinds

pdbr's default inflector (`internal/inflect.Default`) is the identity —
raw PostgreSQL identifiers everywhere — with two rules: computed-column keys
strip the `<table>_` prefix, and relation keys are the referenced /
referencing table name. The kinds a hook can intercept:

| Kind | Input fields | Default |
|---|---|---|
| `resource_path` | `Schema`, `Table` | `Table` |
| `field_name` | `Schema`, `Table`, `Column` | `Column` |
| `relation_forward` | `Schema`, `Table` (referenced), `Constraint`, `Columns` | referenced table name |
| `relation_backward` | `Schema`, `Table` (referencing), `Constraint`, `Columns` | referencing table name |
| `rpc_name` | `Schema`, `Function` | `Function` |
| `computed_field` | `Schema`, `Table`, `Function` | `Function` minus `<Table>_` |
| `enum_value` | `Enum`, `Value` | `Value` |

There is no `simple-names` plugin in pdbr: its whole purpose in pdbq is to
undo the verbose PostGraphile-style defaults (`allUsers` → `users`), and
pdbr's defaults are already the raw names.

## Built-in plugins

| Name | Priority | Hooks | Settings (`plugins.settings.<name>`) |
|---|---|---|---|
| `smart-comments` | 50 | catalog, inflection, api | — |
| `advanced-filters` | 150 | api | `relations` (true), `computed` (true), `relation_order` (true), `relations_opt_in` (false), `computed_opt_in` (false) |
| `postgis` | 160 | api | `filters`, `derived`, `ordering`, `projection` (all true) |
| `nested-mutations` | 200 | api, compile, request | `force_transactions` (true), `max_depth` (3), `max_operations` (50) |

`advanced-filters` and `postgis` are `APIHook` only (pdbq parity: the core
compiler reads the bindings they register, and `internal/compile/spatial.go`
is core per the brief); [cli.md](cli.md) and
[architecture.md](architecture.md) list them the same way.

### smart-comments

Turns PostgreSQL `COMMENT` tags (`@omit`, `@name`, `@primaryKey`, …) into
API customization (full tag reference: [smart-comments.md](smart-comments.md)).
**CatalogHook** (`pdbcore/smartcomments/catalog`, shared with pdbq) parses
and indexes every comment's tags, then edits the catalog: fully-omitted
tables/functions/constraints are removed, `@omit create/update/delete` clear
privilege flags, `@primaryKey`/`@unique`/`@foreignKey` add logical
constraints, `@notNull`/`@nullable` fix view nullability, `@filterable` adds
a synthetic index (the builder's indexed-only allow set). **InflectionHook**
— `@name` replaces the pg identifier at the *head* of the naming chain and
delegates to `next`, so downstream naming plugins derive everything from the
new name; `@fieldName`/`@foreignFieldName` short-circuit `relation_forward` /
`relation_backward` with exact keys. **APIHook** — removes what has no
catalog representation (routes for `@omit read`/`all`, methods, filter and
order params, backward embed keys for `@omit many`, hidden columns and
per-method body keys), marks operations and properties `deprecated`, and
renames enum schemas (tag lines are stripped from OpenAPI descriptions by the
core builder, plugin or not). It runs first (50) so a dropped parameter is
never referenced by `advanced-filters` or `postgis`. Constructor
`smartcomments.New(settings)`; no settings.

### advanced-filters

Shows the "API surface + metadata" pattern: the plugin implements only
`APIHook`, yet changes compiled SQL. Its bindings *are* model flags: the
hook sets `Builder.Surface.{RelationFilters, RelationOrder, ComputedFilters,
ComputedOrder}` from its settings and narrows the per-object flags
`Relation.Filterable`/`Orderable` (every FK direction) and
`Computed.Filterable`/`Orderable` (every computed column). `Build()` copies
them into `api.Built`; `server.ParseOptions` turns the `Surface` flags into
`parse.Options` gates and the parser consults the per-object flags, so
relation filter paths (`posts.title=eq.x`, `posts!none.title=…`,
`posts!every.title=…`, `users.name=eq.x`, `posts=exists.true|false`),
related-column order terms (`order=users(name).asc`; backward relations,
and forward ones over a `PERIOD` FK, aggregate with `MIN`/`MAX`) and computed-column filter/order
(`post_count=gt.3`, `order=post_count.desc`) only ever reach the plan when
the plugin allowed them. The core compiler renders whatever is in the plan
(`EXISTS` subqueries, correlated/aggregate order subqueries, function calls
over the current row) and never looks at the flags — disabling the plugin
leaves `Surface` all-false and the syntax is `400` at parse time. No
`CompileHook` needed. The halves toggle independently (`relations`,
`computed`, `relation_order`, the latter also off when `relations` is);
`relations_opt_in`/`computed_opt_in` flip the surface to
`@filterable`/`@sortable`-only; `@omit filter` / `@omit order` on FK
constraint and function comments are read with `pdbcore/smarttags` straight
from the catalog (independent of `smart-comments`); a flag an earlier plugin
already cleared (`@omit many` → backward `Embeddable=false`) is never turned
back on; set-returning computed columns are never exposed; the computed half
follows `schema.functions`. Computed filters are not index-backed, so they
sidestep `filters.indexed_only` by nature; the related column of a relation
filter or order *does* follow the related table's policy. OpenAPI (M6)
documents relation/computed parameters from the same flags
(`Built.Surface` + `Relation.Filterable`/`Orderable` +
`Computed.Filterable`/`Orderable`). Details: [filtering.md](filtering.md),
[pagination.md](pagination.md).

### postgis

Follows exactly the same shape for PostGIS columns: spatial filter operators
(`st_*`, `bbox_*`), `<col>_<accessor>` pseudo-columns, `select=` projection
arguments (`transform=`, `simplify=`, `buffer=`, `precision=`), `.dist` ordering with `near=` and the
spatial aggregate functions (`col.extent()`, …). The
grammar itself lives in the core parser/compiler behind `api.Surface` gates;
the plugin flips `SpatialFilters`/`SpatialOrder`/`SpatialProjection`/`SpatialAggregates` from its
settings, registers `Resource.Pseudo` entries (`derived`; a real or computed
column owning the name wins, with a warning), applies `@omit filter|order`
from the column comments itself (no smart-comments dependency), and adds
the `GeoJSON` component schema, the `geometry`/`geography` type override and
the spatial `x-pdbr-filter-ops` vocabulary to `Builder.Extra` (the `near`
parameter and per-column filter params render with M6's parameter work).
Nothing happens when the model has no spatial column. Reading and writing
GeoJSON is core (`pdbcore/postgis`), so disabling the plugin only removes the
query surface. See [postgis.md](postgis.md).

### nested-mutations

Exercises every REST-specific hook surface
(`internal/plugins/nestedmutations`). **APIHook** — publishes
`Builder.Surface.NestedKeys` (`nestedmutations.NestedKeys`: every
unambiguous relation key of a resource), which the parser reads through
`parse.Options.NestedKeys` — single-object `POST` bodies and `PATCH` bodies
of a single-row path accept them, and forward-FK columns stop being
required when the key is present — plus `Builder.Surface.NestedSchema`
(`nestedmutations.NestedSchema`: the `.insert` / `.update` property of a key,
listing only the operations it offers there) and a `<name>.connect` key
schema per resource a nested key leads to (`oneOf` the primary key / each
unique constraint; a temporal key without its period column, which `PERIOD`
relations connect by). Forward keys take `create` / `connect` (and, on a
`PATCH`, `disconnect`, `update`, `delete`); backward keys take lists under
`create` / `connect` (and, on a `PATCH`, `update`, `delete`, `disconnect`).
Over a `PERIOD` FK only the equality columns are copied from the related
row ([mutations.md](mutations.md#temporal-period-relations)).
**CompileHook** — when the plan carries nested keys it resolves them
(operation availability per relation and method — privileges, `@omit`,
FK writability, hidden referenced columns, nullable FK for `disconnect` —
nested-in-nested up to `max_depth`, at most `max_operations` operations,
body-rule coercion of every nested row and patch via
`parse.CoerceObjectFor`, values rendered by `compile.InputValue`), replaces
the DML with a multi-CTE statement (`__p_n` / `__d_n` forward operations
first, `__mut` main row — an `INSERT`, an `UPDATE`, or a `SELECT … FOR
UPDATE` lock for a `PATCH` without columns — then `__c_n` backward
operations reading the parent from its CTE) and hands the chain to
`compile.MutationChain`, which reuses the core response compiler
(`select=` embeds included). Rows written in CTEs are invisible to
same-statement table scans, so it also passes, per table, the CTEs holding
rows that are in it after the statement and conditions excluding the rows
it replaced or deleted; the core reads such a table, when the response
embeds it, from a `__vis_n` visibility CTE. Every `connect`, `update` and
`delete` (and the addressed row of a `PATCH`) carries a
`compile.GuardSQL` guard, so a lookup that matches nothing aborts the
statement and answers `404 not_found`. **RequestHook** — sets `op.ForceTx`
when the plan has nested keys (`force_transactions: false` opts out). Depth
beyond `max_depth` (default 3, root = 1) → `400 embed_depth_exceeded`; an
operation the relation does not offer → `400 unknown_relation`. Full rules
and the exact SQL shape:
[mutations.md](mutations.md#nested-mutations-plugin-nested-mutations).

## Extending the API: `api.Builder`

`APIHook`s receive the builder after default generation and before
`Build()`. It exposes the resource model (resources, columns, relations,
RPCs, enums), and per-resource mutators for routes/methods, query
parameters, selectable fields (computed/pseudo-columns with a SQL rendering),
filter/order bindings, request-body schema variants, and raw OpenAPI
additions (`components.schemas`, parameters). Everything a built-in does goes
through this surface, so a third-party plugin can do the same.

The method set is the one the built-ins needed; each entry names the plugin
that introduced it.

| Mutator / field | Effect | Used by |
|---|---|---|
| `Resource(schema, table)`, `Resources`, `RPCs`, `Enums`, `Composites`, `Catalog`, `Inflect`, `Options`, `Warn(msg, attrs…)` | model access | all |
| `RemoveResource(schema, table)` | drop a resource and every relation / RPC pointing at it | core |
| `RemoveMethod(r, methods…)` | turn methods off; `Build` drops them from every route (`405` + `Allow`, or no route when none is left) | smart-comments (`@omit read/create/update/delete`) |
| `Resource.OmitList` | suppress `GET`/`HEAD` on the collection route only | smart-comments (`@omit all`) |
| `RemoveRelation(rel)` | drop one relation candidate; the key vanishes with its last candidate | smart-comments (`@omit many`) |
| `RemoveField(r, key, everywhere)` | hide a column from responses, `select=`, filters, `order=`, `distinct=` and aggregates (and, with `everywhere`, bodies); unique lookups over it lose their `/by/` route, and a hidden primary-key column drops the row path and keyset cursors | smart-comments (column `@omit` / `@omit read`) |
| `Resource.NoCreate` / `NoUpdate` + `Resource.InputColumnsFor(method)` | reject body keys per write method (`PUT` honours both) | smart-comments (column `@omit create/update`) |
| `RemoveFilterColumn(r, key)` / `RemoveOrderColumn(r, key)` | withdraw a key from the filter / `order=` vocabulary | smart-comments (`@omit filter/order`) |
| `RemoveComputed(r, key)`, `RemoveRPC(schema, fn)`, `RemoveUnique(r, constraint)` | drop a computed column, an RPC route, a `/by/` route | smart-comments (available; bare `@omit` is resolved at the catalog level) |
| `RenameEnum(e, name)` | set an enum's `components.schemas` name | smart-comments (`@name` on a type) |
| `Resource/Field/Computed/RPC.Deprecated` + `DeprecationReason` | `deprecated: true` + `x-deprecation-reason` on schemas, properties and operations | smart-comments (`@deprecated`) |
| `Builder.Surface` (`RelationFilters`, `RelationOrder`, `ComputedFilters`, `ComputedOrder`, `Spatial` + `SpatialFilters`/`SpatialOrder`/`SpatialProjection`, `SpatialAggregates`) | grammar gates copied to `Built.Surface` and read by `server.ParseOptions` (`Spatial` implies the three spatial halves; postgis sets the halves from its `filters`/`ordering`/`projection` settings and `SpatialAggregates` from `aggregates`) | advanced-filters, postgis |
| `Relation.Filterable`/`Orderable`/`Embeddable`, `Computed.Filterable`/`Orderable` | per-object gates (default true — `Computed`'s to `!ReturnsSet`); plugins only ever narrow them | smart-comments, advanced-filters |
| `Builder.Surface.NestedKeys func(*Resource) map[string]*Relation` | the nested-input body keys a resource accepts (relation key → relation); copied to `Built.Surface`, read by `server.ParseOptions` → `parse.Options.NestedKeys`; nil = nested keys are unknown body keys | nested-mutations |
| `Builder.Surface.NestedSchema func(*Resource, *Relation, Method) *Schema` | the OpenAPI property of a nested key in the `.insert` (`MethodPost`) or `.update` (`MethodPatch`) body, or nil when the key takes no input there; a forward key's `.insert` property releases its FK columns from `required` | nested-mutations |
| `Builder.Extra.Schemas[name]` + `Builder.ColumnSchema(col)` | add a `components.schemas` entry (rendered verbatim at `Build`), with the core's per-column schema mapping for its properties | postgis (`GeoJSON`), nested-mutations (`<name>.connect`) |
| `Builder.Extra.Types[pgType]` | override the OpenAPI schema of a PostgreSQL type name (`Built.TypeSchema` consults it first; arrays nest) | postgis (`geometry`/`geography` → `$ref GeoJSON`) |
| `Builder.Extra.FilterOps[pgType]` | the operator vocabulary a column of that type documents in `x-pdbr-filter-ops` (and the parameter description) instead of the scalar ops | postgis (`st_*`, `bbox_*`, `is`) |
| `Builder.Extra.InputTypes[pgType]` | override the schema of a PostgreSQL type name in request bodies only (`.insert`/`.update`/`.upsert` properties, RPC `POST` bodies); falls back to `Extra.Types` | postgis (`oneOf [GeoJSON, string]` for WKT input) |
| `Resource.Pseudo[key] = &PseudoColumn{Key, Column, Accessor, Schema, Description}` | a select-only derived field the core compiler renders through `pdbcore/postgis.Deriveds` (`Accessor` = snake-case suffix); `readOnly` property in OpenAPI, excluded from `*`, never filterable/orderable/writable | postgis (`<col>_area`, …) |
| `RemoveFilterColumn` / `RemoveOrderColumn` (see above) | also used to honour `@omit filter|order` read straight from the catalog | postgis |

## Building SQL: the compile helper API

`CompileHook`s that emit their own SQL (rather than just observing) use the
same exported helpers pdbq's `internal/compile/hooks.go` provides, so
parameter numbering and identifier quoting compose with the core compiler:

| Helper | Purpose |
|---|---|
| `compile.ParamSet` — `Add(v any) string`, `Args() []any` | append a bind parameter, get its `$n` placeholder; the core continues numbering after yours |
| `(*ParamSet).SpatialValue(col *introspect.Column, v any) (string, error)` | render a GeoJSON/WKT input for a spatial column as its single-parameter constructor expression (`ST_GeomFromGeoJSON($3)` …); mandatory for spatial columns — a bare parameter would fail type resolution |
| `(*Compiler).MutationWithCTEs(req *Request, ctes string, ps *ParamSet, inserted map[string][]string) (*Statement, error)` | compile the response (`select=`, embeds, RETURNING projection) against a caller-supplied CTE chain that defines `__mut` (a plain INSERT); `inserted` maps `"schema.table"` to the chain's other CTEs holding rows the statement inserted into it (`RETURNING *`), and embeds reading those tables or the mutated one read a visibility CTE (table `UNION ALL` the new rows) for same-snapshot visibility |
| `(*Compiler).MutationChain(req *Request, ch Chain) (*Statement, error)` | the general form: `Chain{CTEs, Params, RootKeep, Writes, Guards}`. `RootKeep` is set when `__mut` updates or locks existing rows (a condition over the bare table keeping the rows it left alone); `Writes["schema.table"]` = `TableWrites{Rows, Keep}` — CTEs returning every column of rows in the table after the statement, and conditions keeping the pre-statement rows the chain left alone; `Guards` are `GuardSQL` conditions evaluated before the response (even an empty one) |
| `compile.GuardSQL(cte, label string) string` / `compile.GuardFailure(msg)` | a guard requiring `cte` to yield a row; a failure raises `22P02` carrying `pdbr guard <token>: <label> matched 0 rows` (`<token>` a per-process secret, masked by `RenderGolden`), which the executor answers as `404 not_found` with `<label> matched 0 rows` as the detail; `GuardFailure` accepts only messages carrying the token (`label` must not depend on request values) |
| `compile.CoerceInput(built *api.Built, col *introspect.Column, v any) any` | convert a JSON body value into a driver-friendly parameter (enum labels, json/jsonb re-encoded as text, arrays element-wise, `int8`/`numeric` strings); route every column value through it before `ParamSet.Add` |
| `(*Compiler).InputValue(col *introspect.Column, v any, ps *ParamSet) (string, error)` | render a (parser-coerced) body value as the parameter expression the core DML uses — composites `jsonb_populate_record(NULL::t, $n::jsonb)`, arrays `$n::type[]`, json `$n::jsonb`, PostGIS constructors, bare `$n` otherwise — appending to `ps`; the one-stop rendering for plugin-built INSERT tuples (nested-mutations) |
| `parse.CoerceObject(built, res, obj)` / `parse.CoerceObjectFor(built, res, obj, method)` / `parse.CoerceValue(built, col, v)` | apply the body-validation rules and coercion table to a nested row (`POST` rules) or patch (`plan.PATCH`: `@omit update` columns rejected) — unknown key / readonly / uncoercible → `UserError` — or to one value regardless of writability (key lookups) |
| `compile.QuoteIdent(s string) string` | identifier quoting — the **only** way identifiers may enter SQL text |
| `compile.TableRefSQL(schema, table string) string` | quoted, schema-qualified relation reference |

The invariant a hook must preserve is the core one: SQL text is a pure
function of request shape; values travel only as parameters. `FuzzFilter` and
`FuzzSpatial` cover the core compiler; a plugin that renders SQL should ship
the equivalent test.

## Related

- [rls.md](rls.md) — what `Operation.Claims`/`Role` mean and how they reach PostgreSQL.
- [smart-comments.md](smart-comments.md), [postgis.md](postgis.md) — the built-ins' user-facing surfaces.
