# Architecture

> **Status: as built (v0.1.0).** This document was written as the design
> spec and has been corrected to match the code where the two diverged; the
> pipeline, package layout, invariants and SQL shapes below are what ships.
> `CLAUDE.md` carries the maintained, more detailed as-built contracts and
> gotchas; where this file and the code disagree, the code (and its goldens)
> wins — please fix the doc.

pdbr is a zero-boilerplate REST API for PostgreSQL: point it at a live
database, it introspects `pg_catalog`, generates a PostgREST-style REST
surface (resources, embeds, filters, pagination, CRUD, RPC, OpenAPI 3.1) and
compiles every HTTP request into **one** parameterized SQL statement that
returns the response JSON in a single `json` column. Same engine philosophy
as [pdbq](https://github.com/suprbdev/pdbq) (GraphQL); the transport-neutral
half of that engine is shared through the
[`pdbcore`](pdbcore.md) module.

Related: [features.md](features.md) (support checklist),
[pdbcore.md](pdbcore.md) (shared module).

## Pipeline

```
pg_catalog
   │  pdbcore/introspect.Introspect  (or pdbcore/cache.Load)
   ▼
introspect.Catalog                 ← the single serializable input to everything
   │  CatalogHooks (plugins)
   │  api.New(cat, inflector, opts) → APIHooks (plugins) → Build()
   ▼
api.Built                          ← resource model + router table + OpenAPI doc + compiler lookup tables
   │  HTTP request → parse (method, path, query, headers, body) → plan.Request
   ▼
plan.Request                       ← transport-neutral query plan (tree of selections/embeds/filters/order/page/mutation)
   │  compile.Func (CompileHooks wrap it)
   ▼
compile.Statement                  ← ONE SQL string + args (+ Mutation flag)
   │  pdbcore/pgexec: tx policy, SET LOCAL ROLE/claims, retry, error classify
   ▼
json `data` (one row, one column) ← PG builds the response JSON; Go only maps status/headers (+CSV encode)
   │
   ▼
server (net/http, atomic *Runtime pointer, hot-swap on watch)
```

Every stage consumes only the output of the previous one. In particular the
compiler never sees `*http.Request`, and the server never sees SQL.

## Package layout

```
pdbr.go            App: New / WithPlugins / WithLogger / WithVersion / WithMetrics / Connect /
                   LoadCatalog / BuildAPI / Runtime / Serve / Handler / CompileSQL / SetReadOnly /
                   Metrics; watch-mode wiring (pdbcore/watch → BuildAPI → Runtime → Server.Swap).
                   Registers built-in plugins.
facade.go          Type aliases that form the public embedding contract (Config sections,
                   hook interfaces, plan/compile/exec types); implementation stays internal.
cmd/pdbr/          cobra CLI: serve, request, schema, config, plugins, completion, --version.
internal/
  config/          Config = pdbcore/confx shared sections + pdbr-owned Server + TX (no per_request);
                   DefaultConfig, Validate (16 pdbq checks − dropped keys + base_path/embed-depth rules),
                   ExampleYAML via confx, flag mirroring for cmd/pdbr.
  api/             Resource model (Resource, RPC, Enum, Composite, Relation, Computed), Builder
                   (mutable, handed to APIHooks), Build() → Built (router table + lookup tables +
                   OpenAPI document). Pure function of catalog + options + hooks.
  parse/           URL grammar → plan.Request: path segments (schema/table/pk/by/rpc), reserved
                   query params, select= tree, filter expressions, order=, cursors, headers
                   (Prefer, If-Match, If-None-Match, Accept), JSON body validation against api.Built.
                   Every failure is a *UserError{Code, Status, Detail} — the fuzz target FuzzParse
                   asserts it never panics.
  plan/            Transport-neutral request plan structs (below). No imports beyond pdbcore/introspect.
  compile/         plan.Request → compile.Statement. Files: compile.go (dispatch + list/row),
                   rows.go (jsonb_build_object / jsonb_agg / laterals), filter.go (incl. spatial ops
                   via pdbcore/postgis), order.go, keyset.go, cost.go, mutations.go
                   (insert/patch/delete/upsert/etag CTEs, MutationWithCTEs), rpc.go, hooks.go
                   (exported helpers for CompileHooks), errors.go, golden.go (RenderGolden),
                   testdata/*.http|*.sql.
  exec/            Thin: exec.go (Executor: hooks, read-only gate, cost check, compile chain,
                   tx policy, pdbcore/pgexec, error mapping), context.go (Operation in ctx),
                   unwrap.go / unwrap_rpc.go / unwrap_mutation.go (Statement.Shape → status,
                   headers, body). Result{Status, Headers, Data, Err}.
  server/          server.go (Server: middleware chain, mux, auth, Built.Match routing, atomic
                   *Runtime + Swap), handler.go (parse → exec → response, CSV, CompileSQL),
                   mutation.go (read-only 405 before body read), problem.go (RFC 9457 writer,
                   codes), timeout.go (504 problem). Uses pdbcore/httpx for CORS/gzip/listen.
  plugin/          pdbr hook set: APIHook, CompileHook, RequestHook + Registry helpers layered on
                   pdbcore/plugin (Plugin, CatalogHook, InflectionHook, Registry).
  plugins/
    smartcomments/ priority 50  — pdbcore/smartcomments/catalog CatalogHook + InflectionHook + APIHook.
    advancedfilters/ priority 150 — APIHook only: relation filters/existence, related order, computed filter/order (bindings read by the core compiler).
    postgis/       priority 160 — APIHook only: spatial filter params, pseudo-columns, projection args, distance order (spatial SQL lives in core via pdbcore/postgis).
    nestedmutations/ priority 200 — nested create/connect/update/delete/disconnect: APIHook (input
                   schemas), CompileHook (multi-CTE + guards), RequestHook (ForceTx).
  inflect/         pdbr Kind constants + Default (identity + computed prefix strip + relation-key rules)
                   on top of pdbcore/inflect helpers.
test/              e2e suite (httptest server over the real handler; PDBR_TEST_DATABASE_URL).
```

Everything transport-neutral (`introspect`, `cache`, `watch`, `smarttags`,
`postgis`, `auth`, `pgexec`, `jwtmint`, `httpx`, `confx`, `plugin` base,
`testutil`) lives in `pdbcore` — see [pdbcore.md](pdbcore.md).

## plan.Request — the transport-neutral query plan

### Why a plan and not the raw request

In pdbq the compiler takes an `*ast.Field` and consults the GraphQL AST only
for: field name / alias, resolved argument values, argument type names,
positional key arguments, the result type, and the child selection list
(after `expandFor` flattens fragments and directives). Everything else it
needs comes from `schema.Built` lookup tables. That is a small enough surface
that a pre-resolved plan removes all AST coupling: pdbr's `parse` package
resolves names to catalog objects **once**, and the compiler operates on
catalog references only. Benefits:

- The compiler is testable and fuzzable without HTTP (`plan.Request` literals
  in tests, `.http` golden files parsed through the same `parse` package).
- CompileHook plugins get a typed tree instead of re-parsing query strings.
- `pdbr request --print-sql` and the server share one code path.
- A future second transport (e.g. gRPC/JSON-RPC over the same plan) needs a
  new `parse` only.

### Structs

As designed; `internal/plan/plan.go` is authoritative and matches this sketch
modulo the postgis value types (`DWithin`, `BBox`), `FieldSel.Node` and the
`Nested` subtree used by the nested_mutations plugin.

```go
package plan

type Method string // GET, HEAD, POST, PATCH, PUT, DELETE (OPTIONS never reaches the plan)

type Request struct {
	Method   Method
	Resource *api.Resource // nil for RPC
	RPC      *api.RPC      // nil for resources
	// Key identifies a single row: primary key values (pk path) or a unique
	// constraint + values (/by/{cols}/{vals}). Nil for list paths.
	Key *Key
	// Body is the parsed and validated JSON body: one row (Object) or a bulk
	// list (Objects) of column→value maps already coerced by pg type;
	// for RPC the arg map. Nested-mutation subtrees ride along as NestedInput.
	Body *Body
	// Select is the projection tree (nil = "*", all columns).
	Select *Node
	Filter Expr // nil = no filter
	Order  []OrderTerm
	Page   Page
	Count  bool
	// Distinct lists DISTINCT ON columns in request order (nil = none).
	Distinct []*introspect.Column
	// OnConflict is the upsert target (nil = primary key).
	OnConflict *introspect.Constraint
	// Near is the reference geometry for distance ordering (postgis plugin).
	Near any
	IfMatch     string // raw ETag from If-Match ("" = absent)
	IfNoneMatch string
	Prefer      Prefer // Return: representation|minimal
	Accept      Accept // JSON | CSV
	// ForcedTx etc. are NOT here — that is exec.Operation's business.
}

type Key struct {
	Constraint *introspect.Constraint // the PK or unique constraint matched
	Values     []any                  // coerced, positional to Constraint.Columns
}

type Body struct {
	Object  map[string]any   // single row / RPC args
	Objects []map[string]any // bulk insert / bulk upsert
	// Nested holds nested-mutation subtrees keyed by relation key, populated
	// only when the nested_mutations plugin is enabled.
	Nested map[string]*Nested
	// Columns is the union of body keys in catalog column order (bulk).
	Columns []*introspect.Column
}

type Node struct {
	Resource *api.Resource
	Alias    string // response key for this node ("" at root)
	Fields   []FieldSel
	Embeds   []Embed
	Star     bool // "*" present: all columns of Resource, plus explicit Fields/Embeds
}

type FieldSel struct {
	Alias    string             // response key ("" = Name)
	Column   *introspect.Column // real column
	Computed *api.Computed      // computed column (row-type function)
	Pseudo   *api.PseudoColumn  // postgis derived accessor (<col>_<accessor>)
	Args     map[string]any     // spatial projection args: transform, simplify (v1)
	Agg      *Aggregate         // aggregate function (server.aggregates); Column = input, nil for count()
}

type Aggregate struct {
	Func       AggFunc // count sum avg max min | extent union collect centroid
	InputCast  string  // col::type.fn()
	OutputCast string  // col.fn()::type
}

type Embed struct {
	Key      string        // relation key as written (author, posts, …)
	Alias    string        // response key ("" = Key)
	Relation *api.Relation // resolved (direction, FK, hint applied)
	Inner    bool          // !inner
	Node     *Node
	Filter   Expr          // per-embed filters (posts.title=eq.x)
	Order    []OrderTerm   // posts.order=
	Page     Page          // posts.limit= / posts.offset=
}

// Expr is a filter tree. Exactly one field is set per node.
type Expr interface{ isExpr() }
type And struct{ Terms []Expr }
type Or struct{ Terms []Expr }
type Not struct{ Term Expr }
type Cond struct {
	Column   *introspect.Column // real column of the current resource
	Computed *api.Computed      // computed-column filter (advanced_filters)
	Relation *RelationPath      // relation filter (advanced_filters)
	Op       Op                 // eq, neq, …, st_intersects, exists
	Value    any                // coerced; nil for structural ops (is.null …)
	Negate   bool               // leading not.
}
type RelationPath struct {
	Steps      []*api.Relation // one step today (rel.col); grammar allows more
	Quantifier Quantifier      // some (default) | none | every | exists
	Leaf       *Cond           // nil for exists.true/false
	Exists     *bool           // for exists.true|false
}

type OrderTerm struct {
	Column   *introspect.Column
	Computed *api.Computed
	Relation *RelationOrder // users(name) — forward: scalar subquery, backward: MIN/MAX
	Distance *introspect.Column // spatial column for `.dist` (uses Request.Near)
	Desc     bool
	NullsFirst bool // both directions default to NULLS LAST
}

type Page struct {
	Limit  int   // always resolved: explicit or server.max_page_size
	Offset int
	After  []any // decoded cursor pk values (nil = absent)
	Before []any
}
```

`plan` imports `pdbcore/introspect` and `pdbr/internal/api` and nothing else.
`compile` imports `plan` + `api`; `parse` imports all three; `server` imports
`parse`, `exec`, `api`. There is no path from `compile` back to `parse`, so a
CompileHook can never depend on URL syntax.

Decided (M2, as built): `plan` references `api.Resource` / `api.Relation` /
`introspect.Column` **pointers** into the `Built` (zero lookups in the
compiler, mirrors pdbq's `FieldMeta.Table` pointer). The plan is not
serializable and does not need to be — `pdbr request --print-sql` re-parses.

### Parse rules the plan encodes

- Path → `Method`, `Resource`/`RPC`, `Key`. Composite PK: comma-separated in
  PK column order, each value percent-decoded. `/by/{cols}/{vals}`: `cols` is
  matched as a set against unique constraints; `Values` are reordered to the
  constraint's column order.
- Reserved query params (`select order limit offset after before count
  distinct near on_conflict`) → dedicated fields; every other param is a
  filter. Dotted params (`posts.title`, `posts.order`, `posts.limit`) attach to
  the embed with that key; a dotted filter whose key is not a selected embed
  is a relation filter (advanced_filters plugin, `EXISTS`); a key that matches
  no FK of the resource → 400 `unknown_relation`.
- `select=` grammar (brief §5) → `Node` tree; depth > `server.max_embed_depth`
  → 400 `embed_depth_exceeded`; ambiguous relation key without hint → 400
  `ambiguous_relation` listing candidates.
- Filter values are coerced by column type here (enum labels verbatim, arrays
  from `{a,b}`, jsonb from JSON text, spatial from GeoJSON/WKT); unknown column
  → 400 `unknown_column`; unknown op → 400 `invalid_filter`; policy violation
  → 400 `column_not_filterable` / `column_not_orderable`.
- Cursors: base64url(no padding) of JSON `["schema.table", pk…]`, decoded
  structurally, PK-typed, resource name must match the requested resource →
  else 400 `invalid_cursor` (code proposed in [errors.md](errors.md), not in
  the brief).
- `limit` absent → `server.max_page_size`; over → 400 `page_size_exceeded`
  (invariant 4). `distinct` with `after`/`before`, `offset` with `before`, and
  keyset on a PK-less resource → 400 `keyset_unsupported`.
- Body: unknown key → 400 `invalid_body`; generated/identity `ALWAYS` → 400
  `column_readonly`; required = `!Column.OmittableOnInsert()` on create; PATCH
  all optional. `{}` on POST → `DEFAULT VALUES`. `int8`/`numeric` accept
  number or string.

## Compiler contract

```go
package compile

type Statement struct {
	SQL      string
	Args     []any
	Mutation bool
}

type Request struct {
	Plan          *plan.Request
	Built         *api.Built
	MaxEmbedDepth int
	MaxPageSize   int
}

type Func func(ctx context.Context, req *Request) (*Statement, error)

type Compiler struct{ Built *api.Built }
func New(b *api.Built) *Compiler
func (c *Compiler) Compile(ctx context.Context, req *Request) (*Statement, error)

// hooks.go — exported for CompileHook plugins (compose param numbering with the core):
type ParamSet struct{ … }        // Add(v) → "$n"; Args(); SpatialValue(col, v)
func (c *Compiler) MutationWithCTEs(req *Request, ctes string, ps *ParamSet, inserted map[string][]string) (*Statement, error)
func (c *Compiler) RowJSON(req *Request, node *plan.Node, alias string, ps *ParamSet) (jsonExpr, laterals string, err error)
func QuoteIdent(s string) string
func TableRefSQL(schema, table string) string
func CoerceInput(built *api.Built, col *introspect.Column, v any) any
```

The statement **always** returns one row with one `jsonb` column named
`data`; a missed single-row lookup returns zero rows and the exec layer maps
that to 404. `HEAD` compiles exactly like `GET` (the server discards the body).

Dispatch (`compile.go`) switches on `(Method, Key != nil, RPC != nil,
Body shape)`:

| Request kind | SQL shape (abridged; `t0`, `t1`… are allocated aliases) |
|---|---|
| list `GET /users` | `SELECT coalesce(json_agg(json_build_object(…) ORDER BY …), '[]') AS data FROM (SELECT … FROM "public"."users" t0 WHERE <filter> ORDER BY <order>, <pk> LIMIT $1 OFFSET $2) t0 <laterals>` |
| row `GET /users/1` | `SELECT json_build_object(…) AS data FROM "public"."users" t0 <laterals> WHERE t0."id" = $1` (zero rows → 404) |
| by-unique `GET /users/by/email/x` | as row, `WHERE t0."email" = $1` (multi-column: `AND` chain in constraint order) |
| embeds | one `LEFT JOIN LATERAL (SELECT json_agg(…) AS data FROM "public"."posts" t1 WHERE t1."author_id" = t0."id" AND <embed filter> ORDER BY … LIMIT $n) t1 ON true` per embed; forward embeds use `json_build_object` and yield `null`; `!inner` additionally emits `AND EXISTS (SELECT 1 FROM … same correlation and filter)` on the parent (or an inner lateral) |
| keyset `after=` | `WHERE (t0."created_at", t0."id") > ($1, $2)` for uniform direction, expanded `OR` chain when mixed directions/nulls; `before=` reverses direction inside the subquery and the outer `ORDER BY` re-reverses; the statement also emits `has_next`/`has_prev` via `LIMIT $n + 1` and a boolean in the wrapper (see below) |
| `count=true` | `json_build_object(…, 'total', (SELECT count(*) FROM "public"."users" t9 WHERE <same filter>), 'n', <row count>, 'rows', <list agg>)` — the wrapper object is unwrapped by exec (`X-Total-Count`; `rows` last, so its bytes become the body without being decoded); distinct-aware: `count(*) FROM (SELECT DISTINCT ON (...) 1 …)` |
| aggregates (`select=author_id,count()`) | grouped scan `SELECT t0."author_id" AS __k0, count(*) AS __a0 FROM … WHERE … GROUP BY 1` (plain columns raw, computed fields as `to_jsonb`; no embeds; casts `::"pg_catalog"."int4"` from an allowlist) wrapped by `SELECT json_build_object('author_id', g."__k0", 'count', to_json(g."__a0")) AS data, row_number() OVER (ORDER BY <requested keys>, <remaining keys>) … LIMIT $n+1 OFFSET $m` and the usual page wrapper (no cursors; `total` = `count(*)` over a key-only `SELECT 1 … GROUP BY <keys>`); an aggregated to-many embed is the same grouped scan inside its lateral, correlated in the inner `WHERE` ([aggregates.md](aggregates.md)) |
| `distinct=` | inner `SELECT DISTINCT ON (t0."a", t0."b") … ORDER BY t0."a", t0."b", <rest>, <pk>` wrapped by the outer agg that re-applies the requested order |
| `POST` (create) | `WITH __mut AS (INSERT INTO "public"."users" ("email","full_name") VALUES ($1,$2) RETURNING "id","email",…) SELECT json_build_object('row', json_build_object(…), 'key', jsonb_build_array(t0."id")) AS data FROM __mut t0 <laterals>` (`key` only for a single-column PK; exec builds `Location` from it) — bulk: `VALUES ($1,$2),($3,DEFAULT)` + `coalesce(json_agg(…),'[]')`, no wrapper |
| `PATCH` by key | `WITH __mut AS (UPDATE "public"."users" SET "full_name" = $1 WHERE "public"."users"."id" = $2 RETURNING …) SELECT … FROM __mut t0` (row verbatim; `return=minimal` → `RETURNING 1` + `{'n': count}`) |
| `DELETE` by key | `WITH __mut AS (DELETE FROM … WHERE … RETURNING …) SELECT …` |
| bulk `PATCH`/`DELETE` by filter | `WITH __mut AS (UPDATE "public"."users" m0 SET … WHERE <filter over m0> RETURNING …) SELECT json_build_object('n', count(*), 'rows', coalesce(json_agg(s.data), '[]')) FROM (SELECT … FROM __mut t1 <laterals>) s` — shape `mut_bulk`; no filter → `400 unfiltered_mutation` in the parser unless `server.allow_unfiltered_mutations` |
| `PUT` (upsert) | `WITH __mut AS (INSERT … VALUES … ON CONFLICT ("email") DO UPDATE SET "full_name" = EXCLUDED."full_name", … RETURNING …, (xmax = 0) AS __inserted) SELECT json_build_object('row', …, 'inserted', t0.__inserted, 'key', jsonb_build_array(t0."id"))` (single) / `json_agg` (array, always 200) |
| `POST` + `Prefer: resolution=` / `PUT {pk}` | the `POST` / `PUT` shapes with `ON CONFLICT (<on_conflict or PK>) DO UPDATE SET …` (merge) or `DO NOTHING` (ignore) appended to the `INSERT`; a single-row `PUT` takes its conflict target and key values from the path |
| `If-Match` | `WITH cur AS (SELECT md5(to_jsonb(ROW(__cur."id", …))::text) AS h FROM "public"."users" __cur WHERE __cur."id" = $2), __mut AS (UPDATE … WHERE "public"."users"."id" = $2 AND (SELECT h FROM cur) = $3 RETURNING …) SELECT json_build_object('found', EXISTS (SELECT 1 FROM cur), 'row', (SELECT json_build_object(…) FROM __mut t0)) AS data` → exec: `!found` → 404, `found && row IS NULL` → 412 (`= ANY($3)` for several tags; `*` drops the hash predicate) |
| single-row `GET` ETag | `SELECT json_build_object('etag', md5(to_jsonb(ROW(t0."id", t0."email", …))::text), 'row', json_build_object(…)) AS data FROM …` — hash over the resource's **exposed** columns (`Resource.Columns`, so `@omit`/`@omit read` columns never reach it) regardless of `select=` |
| RPC scalar / composite | `SELECT to_json(__fn.v) AS data FROM (SELECT "public"."add_numbers"("a" => $1::"int4", "b" => $2::"int4") AS v) AS __fn` (named notation via `introspect.Function.CallSQL`: omitted args take their `DEFAULT`, `null` → `NULL::type`, variadic functions positional with `VARIADIC`; composites expanded per attribute like composite columns; `auth.mint_type` composites raw → shapes `jwt`/`jwt_set`) — shape `scalar` |
| RPC void | `SELECT 'null'::jsonb AS data FROM (SELECT "public"."f"($1)) AS __v` — shape `void`, exec emits `204` because of the shape, never because of the value. Two statements (`SELECT f(); SELECT …`) are not allowed |
| RPC single row | `SELECT CASE WHEN t0 IS NULL THEN NULL ELSE json_build_object(…) END AS data FROM "public"."publish_post"($1::"int4") AS t0 <laterals>` — shape `scalar` (NULL row → body `null`, 200) |
| RPC `SETOF <table>` | the list statement (`page` shape) with the root scan replaced by `FROM "public"."search_posts"("term" => $1::"text") t0` (`stmt.rootSource`), the resource's filters/order/limit/offset/count, cursors off (offset `Link`s only) |
| RPC `SETOF <scalar>` / `SETOF <composite>` | `SELECT coalesce(json_agg(<value>), '[]') AS data FROM (SELECT __v.v FROM "public"."f"($1) AS __v(v) LIMIT $2 [OFFSET $3]) AS __s` — shape `scalar` |
| nested mutations | `WITH __p_1 AS (INSERT INTO users … RETURNING *), __mut AS (INSERT INTO posts (author_id, …) SELECT __p_1.id, $n FROM __p_1 RETURNING …), __c_2 AS (INSERT INTO comments … FROM __mut RETURNING *), __vis_1 AS NOT MATERIALIZED (SELECT <cols> FROM comments UNION ALL SELECT <cols> FROM __c_2) SELECT … FROM __mut t0 <laterals reading __vis_*>` — built by the nested_mutations CompileHook via `MutationChain`; written rows are `UNION ALL`ed with the table (minus the rows the chain replaced or deleted) into a visibility CTE so embeds in the response see them, and `__guard AS MATERIALIZED (…)` makes a nested lookup that matched nothing abort the statement |
| mutation embed back to the mutated table | `__vis AS NOT MATERIALIZED (SELECT <cols> FROM t WHERE <rows the DML left alone> UNION ALL SELECT __mut.<cols> FROM __mut)` (no `UNION ALL` for `DELETE`), emitted only when an embed reads `t` ([mutations.md](mutations.md#embeds-back-to-the-mutated-table)) |

Notes:

- Lateral correlation is rendered by `listInner` through a callback once the
  child alias is allocated — no textual placeholder, so user-chosen JSON keys
  can never collide with it (pdbq's `%%ALIAS%%` trick is deliberately not
  used).
- `json_build_object` takes at most 100 arguments; rows with more than ~40
  selected pairs are chunked (`json_build_object(…) || json_build_object(…)`).
- Filter keys are sorted before rendering so equal filter sets produce equal
  SQL (invariant 5).
- The wrapper-object convention (`{'rows','total'}`, `{'etag','row'}`, and
  the mutation wrapper `{'row','key','inserted','found','n'}` with members
  present per request) is the only place where SQL returns
  something other than the client-visible JSON; exec unwraps it based on the
  plan (it knows which wrapper to expect). Plain list/row requests without
  those features return the client JSON verbatim so the fast path stays a
  straight `w.Write`.

Void RPCs compile to `SELECT 'null'::jsonb AS data FROM (SELECT f($1)) AS __v`
with shape `void` (see [rpc.md](rpc.md)); exec emits `204` for that shape,
ignoring the value.

## Exec layer

`internal/exec` is thin glue over `pdbcore/pgexec`.

```go
type Operation struct {
	Method    string
	Path      string
	Plan      *plan.Request
	Resource  *api.Resource // or
	RPC       *api.RPC
	Claims    map[string]any
	Role      string
	Mutation  bool
	ForceTx   bool
}

type Result struct {
	Status  int
	Headers http.Header      // ETag, X-Total-Count, X-Next-Cursor, X-Prev-Cursor, Link, Location, Allow
	Data    json.RawMessage  // the client JSON (already unwrapped); nil for 204/304/HEAD or on error
	Err     error            // *Problem-convertible; server renders problem+json
}
```

(Same shape [plugins.md](plugins.md) assumes for `AfterRequest`; hooks
observe but cannot rewrite `Data`.)

Transaction decision table (evaluated after RequestHooks ran, once per request):

| Mutation | role or claim settings to apply | `transactions.mutations` | `ForceTx` | Runs as |
|---|---|---|---|---|
| no | no | — | no | pool query, no tx |
| no | yes | — | — | tx (needed for `SET LOCAL`) |
| no | no | — | yes | tx |
| yes | — | true (default) | — | tx |
| yes | no | false | no | pool query (DML autocommits) |
| yes | yes | false | — | tx (SET LOCAL forces it) |

Inside a tx (`pgexec.QueryTx`): one pipelined batch, so one network round
trip per request. The identity is a single statement, `SELECT
set_config('role', $1, true), (SELECT count(set_config(k, v, true)) FROM
unnest($2, $3))` (what `SET LOCAL ROLE` runs, with every claim as
`<prefix>.<k>`; invariant 3; role validated by `pgexec.ValidRole`, claim keys
validated), followed by the statement. Under `transactions.isolation:
read_committed` (the default) the batch runs in PostgreSQL's implicit
pipeline transaction at the server's `default_transaction_isolation`;
`repeatable_read` / `serializable` wrap it in `BEGIN ISOLATION LEVEL …` /
`COMMIT` in the same batch. A `Prefer: max-affected` write holds the
`COMMIT` back until the affected-row check passed (a second round trip). A
rolled-back write (`server.tx_end` / `Prefer: tx=rollback`) uses the same
hold-back: the scan returns a sentinel error, `QueryTx` rolls back, and exec
treats the sentinel as success. `Prefer: timezone=` queues
`SELECT set_config('timezone', $1, true)` between the identity and the
request statement.
A bulk `POST` / `PUT` under `Prefer: partial=items` is the exception to
"one statement per request": it runs element by element through
`pgexec.RunTx`, each in a `pgexec.Savepoint` ([mutations.md](mutations.md#partial-success-for-bulk-writes)).
Any error → rollback. Serialization failure / deadlock (`40001` / `40P01`)
→ whole batch re-sent up to `transactions.max_retries` times, no backoff;
still failing → 409.

Read-only gate: `App.readOnly` (atomic) is consulted in exec before compile
for every mutation → 405 `read_only` + `Allow` (survives runtime swaps).

Cost check: computed on `plan.Request` (1 + Σ mult × child; mult = explicit
limit clamped to page cap, else 10 for backward embeds without limit;
`@costMultiplier` lowers) → 400 `cost_exceeded` before compile.

Error classification (`pgexec.Classify`) → status per brief §8; the problem
body is assembled in `server` from `Result.Problem`.

## Server layer

- **Router table** comes from `api.Built.Routes`: an ordered list of
  `Route{Method, Pattern, Kind, Resource/RPC, Allow []string}` registered on a
  `net/http.ServeMux` (Go 1.22+ patterns: `GET /api/{schema}/{table}/{pk}`
  etc.). Path segments are matched against the resource index at request
  time (bare aliases for `schema.schemas[0]`; reserved segments `rpc`,
  `openapi.json`, `healthz`, `readyz`, `by` win over bare aliases). Unknown
  resource → 404; known resource + method not offered → 405 with `Allow`.
- **Runtime** = `{Built *api.Built, OpenAPI []byte, Handler RouteHandler}`
  (the `RouteHandler` is `server.Handler`, which owns the `exec.Executor`)
  behind an `atomic.Pointer[Runtime]`; watch mode builds a new Runtime and
  swaps; in-flight requests keep the old pointer (invariant 7,
  `TestSwapKeepsInFlightRequests`).
- **Content negotiation**: request bodies must be `application/json` (415);
  responses `application/json; charset=utf-8`; `Accept: text/csv` on lists
  and set-returning RPCs streams CSV from the jsonb array (embeds present →
  406); other unsatisfiable `Accept` → 406.
- **Errors**: RFC 9457 `application/problem+json` with `code` extension
  (brief §8); `instance` = request path.
- **Headers**: `ETag`/`If-None-Match` → 304 (single-row GET), `Prefer:
  return=minimal|representation`, `Location` on 201, `X-Total-Count`,
  `X-Next-Cursor`, `X-Prev-Cursor`, `Link` built from the request URL.
- **Middleware order** (outermost first): metrics (pdbcore/metrics, when
  enabled: route class, method, status class) → recover → timeout
  (`server.request_timeout` → 504 problem) → CORS (pdbcore/httpx) → gzip
  (opt-in) → max body (413) → auth (pdbcore/auth; 401 problem) → router.
  `{base}/openapi.json` and `metrics.path` are answered before the timeout.
- `/healthz`, `/readyz` are mounted at root, outside `base_path`.

## api.Built model

`api.New(cat, inflector, opts)` returns a mutable `*api.Builder`; APIHooks
mutate it; `Build()` freezes it into `*api.Built`.

```go
type Builder struct {
	Catalog   *introspect.Catalog
	Inflect   inflect.Next
	Options   Options // FilterIndexedOnly, AllowColumns, Functions, BasePath, Schemas, MaxPageSize, Logger
	Resources map[string]*Resource // "schema.table"
	RPCs      map[string]*RPC      // "schema.fn"
	Enums     map[string]*Enum
	Composites map[string]*Composite
	Extra     OpenAPIExtras        // plugin-contributed params/schemas/extensions
}

type Resource struct {
	Table      *introspect.Table
	Path       string            // "users" or "audit.users" (OpenAPI schema name)
	Bare       bool              // has a bare alias
	Methods    map[Method]bool   // GET/HEAD/POST/PATCH/PUT/DELETE after privilege + @omit
	Columns    []*Field          // exposed columns in catalog order (Field{Column, Key, ReadOnly, Deprecated, Description})
	Computed   map[string]*Computed // key → row-type function
	Pseudo     map[string]*PseudoColumn // postgis derived accessors
	Relations  map[string][]*Relation   // key → candidates (len>1 ⇒ hint required)
	Uniques    []*introspect.Constraint // /by/… lookups
	FilterColumns map[string]bool      // policy-resolved
	OrderColumns  map[string]bool
	InputColumns  map[string]*introspect.Column // insert/update-able by JSON key
	Keyset     bool                     // has PK
	Etag       bool
	CostMultipliers map[string]int      // relation key → @costMultiplier
	Description string
	Deprecated  bool
}

type Relation struct {
	Key        string
	Forward    bool
	FK         *introspect.ForeignKey
	From, To   *Resource
	Hint       string // fk name or column, when required
	Filterable, Orderable, Embeddable bool
}

type Built struct {
	Catalog    *introspect.Catalog
	Resources  map[string]*Resource // "schema.table"
	ByPath     map[string]*Resource // "users", "audit.users" (bare aliases resolved)
	RPCs       map[string]*RPC
	RPCByPath  map[string]*RPC
	Enums, Composites …
	Routes     []Route              // deterministic order
	OpenAPI    *openapi.Document    // built once; JSON/YAML rendered on demand
	Warnings   []string             // collisions, suppressed aliases
}
```

Analogues to pdbq's `schema.Built` lookup tables: `FilterColumns` /
`OrderColumns` (was `OrderBy` enum map), `Relations` keyed by relation key +
hint (was `FilterRelations` / `OrderRelation`), `Computed` (was
`FilterComputed`), `InputColumns` (same name), plus the OpenAPI document,
which pdbq did not need (SDL served that role).

## Invariants

1. **One statement per request.** Embeds → `LEFT JOIN LATERAL`, lists →
   `json_agg`, rows → `json_build_object` (chunked ≤40 pairs), mutations →
   `WITH __mut AS (DML RETURNING …) SELECT …`, nested mutations → multi-CTE.
   Zero rows on a single-row path → 404 (query and mutation alike). No N+1,
   no Go-side row marshaling.
2. **SQL text is a pure function of request shape.** Values only travel as
   parameters (`$n`); enforced by fuzz tests. Identifiers only via
   `quoteIdent`. The only structural literals: `is.null`/`is.true`/`is.false`,
   `exists.true/false`, SRID ints, jsonb build keys, and `LIMIT` values
   (Go integers capped by `server.max_page_size`, written as literals so
   Postgres can reuse a generic plan; `OFFSET` stays a parameter).
3. **RLS via `SET LOCAL` only**, claims via `set_config(prefix.k, v, true)`;
   role validated `[A-Za-z0-9_]+`.
4. **No unbounded scans**: absent `limit` → `server.max_page_size`; over →
   400.
5. **Determinism**: routes, OpenAPI, SQL (sorted filter keys) are
   deterministic; golden tests diff byte-for-byte.
6. **OpenAPI is a pure function of `api.Built`** (thus of catalog + config +
   plugins) — the cache stores the catalog, never the OpenAPI.
7. **Hot swap**: watch rebuilds `api.Built` + runtime and swaps an atomic
   pointer; in-flight requests finish on the old one.

## Gotchas

These were written down before the code existed; every one of them held. The
longer as-built list (jsonb spacing, identity columns, RETURNING scans, …)
lives in `CLAUDE.md` § Gotchas.

1. **`pdbcore/testutil.FixtureCatalog()` mirrors `FixtureSQL`.** Both live in
   pdbcore and both products test against them. Change one → change the
   other, in the same pdbcore PR, and re-run both products' goldens.
2. **`make test-e2e` uses `-p pdbr-test`.** Compose project name isolates the
   e2e stack (`down -v`) from `make dev` (port 5433 for the e2e DB).
3. **Golden SQL must be regenerated after compiler or `api` changes.**
   `go test ./internal/compile/ -update`; review the diff — a changed golden
   is a behaviour change.
4. **Env-var key mapping escape.** `PDBR_` stripped, `_` → `.`, `__` → literal
   `_`: `PDBR_FILTERS_INDEXED__ONLY` → `filters.indexed_only`,
   `PDBR_SERVER_BASE__PATH` → `server.base_path`.
5. **Cursors are base64url JSON arrays decoded structurally, never compared
   textually.** `["public.posts", 1]` — json text rendering inserts spaces;
   must stay harmless. Not interchangeable with pdbq `nodeId` (type name
   differs).
6. **SQL-injection invariant enforced by `FuzzFilter`/`FuzzSpatial`.** Never
   interpolate a value; use `ps.Add(v)`. Request-supplied response keys
   (`select=` aliases, JSON-path keys) are values too (`$n::text`). Only
   catalog names are rendered as literals, through `quoteLiteral`, which
   switches to `E'…'` when the name holds a backslash so the literal reads
   the same whatever `standard_conforming_strings` is.
7. **`SET LOCAL` only, never `SET`.** Pooled connections must not leak
   identity. `pdbr serve` warns loudly when `roles.switch` is off.
8. **Reserved query params** (`select order limit offset after before count
   distinct near on_conflict`) can never be bare filters. A column named
   `select` is filterable only inside `and=(select.eq.x)`.
9. **Bare-alias collisions.** A table in `schema.schemas[0]` named `rpc`,
   `by`, `healthz`, `readyz` or `openapi.json` loses its bare alias (startup
   warning); the schema-qualified path still works. Two schemas do not
   collide because only `schemas[0]` gets bare aliases.
10. **ETag hashes every exposed column.** `md5(to_jsonb(ROW(<exposed
    columns>))::text)` needs SELECT on each of them; with column-level grants
    a single-row GET can 403 where the same `select=` list would succeed →
    set `server.etags: false` (or grant, or hide the column). Hidden columns
    stay out of the hash (they would leak through it).
11. **No textual placeholders in rendered SQL that also carries user JSON
    keys.** Correlations are rendered via a callback after alias allocation;
    `%%RETURNING%%` is substituted only in the CTE chain, never in the
    response body; `returningList` scans SQL with string literals stripped.
12. **`pdbr request` runs with `auth.mode: none` and `roles.switch:
    false`.** One-off CLI requests do no auth / role switch by design (so do
    `schema dump|check` and `plugins list`).
13. **Wrapper objects in SQL** (`rows/total`, `etag/row`, and the mutation
    wrapper `row/key/inserted/found/n`) must be unwrapped in exec exactly when the plan asked
    for them; a plan flag and the SQL shape must agree or a client sees the
    wrapper.
14. **`HEAD` must compile identically to `GET`.** Any divergence breaks
    `X-Total-Count` parity and the goldens for `HEAD` files.

## Where to look first

| Task | Start here |
|---|---|
| Change generated SQL shape | `internal/compile/compile.go` (dispatch) → `rows.go` / `mutations.go` / `filter.go` / `keyset.go` / `rpc.go`; regenerate goldens |
| Change URL grammar or a query param | `internal/parse/` (`path.go`, `query.go`, `selectp.go`, `filter.go`, `order.go`, `cursor.go`, `headers.go`, `body.go`, `rpc.go`); add `.http` goldens; run `FuzzParse` |
| Add/change a route, resource field or OpenAPI piece | `internal/api/builder.go` (model) + `internal/api/match.go` (URL resolution) + `internal/api/openapi*.go`; routes are consumed by `internal/server/server.go` (`handleAPI`) |
| Change a generated name/key | `internal/inflect/inflect.go` (`Default`) or a plugin's `Inflect` |
| Add a config option | field + `koanf`/`doc` tags in `internal/config/config.go` (or `pdbcore/confx` if shared); `DefaultConfig`/`Validate`; mirror flag in `cmd/pdbr`; `make example-config`; add a row to `docs/configuration.md` |
| Status code / problem mapping | `pdbcore/pgexec/classify.go` (PG classes) + `internal/server/problem.go` (status table, `code`) |
| Write a plugin | `internal/plugin/plugin.go` (interfaces) + `internal/plugins/nestedmutations` (worked example: APIHook + CompileHook + RequestHook) and `internal/plugins/postgis` (APIHook only) + `docs/plugins.md` |
| RLS / auth / claims | `pdbcore/pgexec` (`QueryTx`, `ValidRole`) + `pdbcore/auth` |
| ETag / If-Match / CSV / Prefer | `internal/server/handler.go` (negotiation, CSV) + `internal/exec/unwrap*.go` (headers) + `internal/compile/mutations.go` (`If-Match` CTE) |
| Schema cache / CI drift gate | `pdbcore/cache`, `cmd/pdbr/main.go` (`schema dump\|check`, `schemaDrift`), tests `cmd/pdbr/schema_test.go`, `test/e2e_cache_test.go` |
| Watch mode / hot swap | `pdbcore/watch` + `pdbr.go` (`buildServer`: `Watcher{TriggerName: "pdbr_watch"}` → `Server.Swap`) + `internal/server/server.go`; e2e `test/e2e_watch_test.go` |
| Build / release / CI | `Makefile`, `Dockerfile`, `compose*.yaml`, `.github/workflows/{ci,release}.yaml`, [deployment.md](deployment.md#releasing) |
