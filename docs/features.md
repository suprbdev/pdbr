# Feature support

Checklist of PostgreSQL and REST features pdbr supports. **As built (v0.1.0): every v1 row is implemented and tested; unticked rows are the v1.x / v2 / stretch backlog.** Checked = implemented and tested; unchecked = planned or explicitly deferred (tier noted where one applies: v1 = first release, v1.x = next minor releases, v2 = major-version track, stretch = nice-to-have). Each item is tagged `(parity)` when it mirrors a pdbq feature or `(new)` when it exists only because of REST. Use this as the reference when adding features: tick the box in the same PR that ships it. See [architecture.md](architecture.md) for how the pieces fit and [PLAN.md](../PLAN.md) for sequencing.

## Introspection (pg_catalog — from pdbcore)

- [x] Tables (including partitioned tables) (v1) (parity)
- [x] Views (read-only) (v1) (parity)
- [x] Materialized views (read-only) (v1) (parity)
- [x] Columns: type, nullability, defaults, identity/generated detection (v1) (parity)
- [x] Primary keys (v1) (parity)
- [x] Unique constraints (v1) (parity)
- [x] Foreign keys (including multi-column) (v1) (parity)
- [x] Indexes (name, columns, uniqueness, method) — drives the filter policy (v1) (parity)
- [x] Enums (v1) (parity)
- [x] Functions (args, return type, set-returning, volatility) (v1) (parity)
- [x] Comments on tables, columns, enums, functions (exposed as OpenAPI descriptions), and constraints (smart comments) (v1) (parity)
- [x] RLS-enabled status per table (v1) (parity)
- [x] Granted privileges per table (SELECT/INSERT/UPDATE/DELETE gate route generation) (v1) (parity)
- [x] Schema allowlist (`schema.schemas`) (v1) (parity)
- [x] Composite types read into the catalog (v1) (parity)
- [x] Composite-type columns mapped to OpenAPI object schemas (nested objects in bodies; composite arrays included) (v1) (parity)
- [x] `CatalogFormatVersion` shared with pdbq; cache files interchangeable (v1) (new)
- [ ] Domains surfaced as named schemas (v1 resolves them to the base type) (v1.x) (parity)
- [ ] Function argument default values / variadic args (v1.x) (parity)
- [ ] Functions with `OUT`/`INOUT` parameters or anonymous record returns (v1.x) (parity)

## Type mapping (Postgres → JSON/OpenAPI)

- [x] `int2`/`int4` → `integer` (`format: int32`) (v1) (parity)
- [x] `int8` → `string` (string-serialized, no precision loss; documented) (v1) (parity)
- [x] `float4`/`float8` → `number` (v1) (parity)
- [x] `numeric`/`money` → `string` (string-serialized) (v1) (parity)
- [x] `bool` → `boolean` (v1) (parity)
- [x] `text`/`varchar`/`char`/`citext`/`name` → `string` (v1) (parity)
- [x] `uuid` → `string` `format: uuid` (v1) (parity)
- [x] `json`/`jsonb` → `{}` (any) (v1) (parity)
- [x] `timestamp`/`timestamptz` → `string` `format: date-time` (v1) (parity)
- [x] `date` → `format: date`, `time`/`timetz` → `format: time` (v1) (parity)
- [x] `bytea` → `string` `contentEncoding: base64` (v1) (parity)
- [x] Arrays of any mapped type → `array` (v1) (parity)
- [x] Enums → `enum` with pg labels verbatim (round-tripped on input and output) (v1) (new)
- [x] Nullable columns → `type: [T, "null"]` (v1) (new)
- [x] `int8`/`numeric` accepted as JSON number **or** string on input (v1) (new)
- [x] `inet`/`cidr`/`macaddr`, `interval`, ranges, `ltree`, `tsvector` → `string` fallback (v1) (parity)
- [ ] `interval` as structured schema (v1.x) (parity)
- [ ] Range types with structured bounds (v1.x; range filter operators tracked under Filtering & ordering) (parity)
- [ ] `hstore` (v1.x) (parity)
- [ ] `ltree` operators (v1.x) (parity)
- [ ] Full-text search: `tsvector`/`tsquery` filter ops (v1.x) (parity)
- [x] PostGIS `geometry`/`geography` → `GeoJSON` schema (GeoJSON out; GeoJSON or WKT/EWKT in), with spatial filters, derived accessors, projection arguments and distance ordering — see [PostGIS](postgis.md) (v1) (parity)

## Reads

- [x] `GET {base}/{table}` → JSON array `200` (v1) (parity)
- [x] `HEAD {base}/{table}` — same headers (incl. `X-Total-Count`), no body (v1) (new)
- [x] `GET {base}/{table}/{pk}` → object `200`; none → `404` problem; composite PK comma-separated in PK column order, percent-encoded (v1) (parity)
- [x] `GET {base}/{table}/by/{cols}/{vals}` — lookup per unique constraint, `cols` order-insensitive, `vals` positional (v1) (parity)
- [x] `GET {base}/{schema}/{table}…` schema-qualified forms; bare alias only for `schema.schemas[0]` (v1) (new)
- [x] Bare aliases suppressed with warning on reserved-segment collision (`rpc`, `openapi.json`, `healthz`, `readyz`, `by`) (v1) (new)
- [x] `select=` projection: columns, `*`, `alias:field`, computed columns, PostGIS pseudo-columns (v1) (parity)
- [x] PostGIS accessors `_x`, `_y`, `_z`, `_m`, `_num_points`, `_num_geometries`, `_start_point`, `_end_point`, `_point_on_surface`, `_wkt`, `_ewkt`, `_is_empty`, `_is_simple`, `_xmin`/`_ymin`/`_xmax`/`_ymax`; type-specific ones generated only where the column's typmod allows a non-null value (`introspect.Column.Spatial`) — see [PostGIS](postgis.md#derived-accessors-pseudo-columns) (v1.x) (parity)
- [x] Embeds `rel(select)` compiled to `LEFT JOIN LATERAL` — forward → object or `null`, backward → array (v1) (parity)
- [x] Arbitrarily nested embeds up to `server.max_embed_depth` (default 5) → else 400 `embed_depth_exceeded` (v1) (parity)
- [x] Relation keys = table names; `!fk_name` / `!column` hints for ambiguous relations; unhinted ambiguity → 400 `ambiguous_relation` listing candidates (v1) (new)
- [x] `!inner` hint — embed filters also restrict the parent (v1) (new)
- [x] Per-embed params on backward embeds: `posts.order=`, `posts.limit=`, `posts.offset=`, `posts.<col>=op.v` (v1) (parity)
- [x] Forward-embed filters null out the embed unless `!inner` (v1) (new)
- [x] Field aliases `alias:field` and embed aliases `author:users(name)` (v1) (parity)
- [x] `select=` applies to mutation responses (compiled against `__mut`) (v1) (parity)
- [x] Single-row weak `ETag: W/"<md5>"` computed in SQL over all columns; `If-None-Match` → `304`; `server.etags` toggle (v1) (new)
- [x] `Accept: text/csv` on lists and set-returning RPCs → RFC 4180 CSV, header row, arrays/json/composites as JSON text, NULL → empty; embeds → `406`; `server.csv` toggle (v1) (new)
- [x] `Prefer: return=representation|minimal` (v1) (new)
- [ ] Aggregate endpoints (count/sum/avg on lists) — stretch (parity)
- [ ] Other `Prefer` tokens (`count=`, `resolution=`) (v1.x) (new)

## Filtering & ordering

- [x] `{col}=[not.]{op}.{value}` grammar; unknown column → 400 `unknown_column`; unknown op → 400 `invalid_filter` (v1) (new)
- [x] Scalar operators: `eq neq gt gte lt lte in is` (`is.null|true|false`) (v1) (parity)
- [x] `in.(a,b,"c,d")` / `not.in.(…)` → `= ANY($n)` / `<> ALL($n)` (v1) (parity)
- [x] Text operators: `like ilike sw ew` (`*` → `%`; `sw`/`ew` operands escaped) (v1) (parity)
- [x] Array operators: `cs cd ov` (`{a,b}` literal) (v1) (parity)
- [x] `jsonb` operators: `cs cd hk jpe jpm` (v1) (parity)
- [x] Enum filtering with pg labels verbatim (v1) (parity)
- [x] Logic: `and=(…)`, `or=(…)`, `not.and=`, `not.or=`, arbitrarily nested; quoted values with backslash escapes (v1) (parity)
- [x] Values coerced by column type at parse time and passed as `$n` params (v1) (parity)
- [x] Indexed-only policy (default): only indexed/PK/unique columns filterable and orderable → else 400 `column_not_filterable` / `column_not_orderable` (v1) (parity)
- [x] Per-table column allowlist override (`filters.allow_columns`) (v1) (parity)
- [x] Global policy switch (`filters.indexed_only: false`) (v1) (parity)
- [x] `@filterable` / `@sortable` smart-comment opt-in (v1) (parity)
- [x] Filters on embedded resources respect the embedded table's own policy (v1) (parity)
- [x] `order=col.asc|desc[.nullsfirst|nullslast],…`; NULLS LAST default both directions; PK tiebreaker always appended (v1) (parity)
- [x] Filtering across relations (`posts.title=eq.x`, `posts!none.`, `posts!every.`, `users.name=eq.x`) — `advanced-filters` plugin, EXISTS subqueries; one level deep in v1 (v1) (parity)
- [x] Relation existence (`posts=exists.true|false`, `users=exists.false`) (v1) (parity)
- [x] Filtering/ordering on computed columns (`post_count=gt.3`, `order=post_count.desc`) — `advanced-filters` plugin (v1) (parity)
- [x] Related-column ordering `order=users(name).asc` (forward: correlated subquery; backward: MIN/MAX) — `advanced-filters` plugin, policy of the related table applies (v1) (parity)
- [x] Spatial filter ops (`st_intersects` … `st_equals`, `st_dwithin`, `st_beyond`, `bbox_*`) — `postgis` plugin (v1) (parity)
- [x] Spatial values parsed for their column's typmod SRID (`pdbcore/postgis.ParseFor`): bare WKT tagged with it, GeoJSON reprojected to it on geometry columns, EWKT kept — filters, `near=`, bodies — see [PostGIS](postgis.md) (v1.x) (parity)
- [x] Directional/equality bounding-box ops (`bbox_leftof`, `bbox_rightof`, `bbox_below`, `bbox_above`, `bbox_overlapsor{leftof,rightof,below,above}`, `bbox_equals`, `bbox_intersectsnd`) and `st_exactlyequals`, `st_orderingequals`, `st_intersects3d`, geometry operand — `postgis` plugin (v1.x) (parity)
- [x] Distance ordering `order=location.dist.asc&near=…`; missing `near` → 400 (v1) (parity)
- [x] `distinct=col1,col2` → `SELECT DISTINCT ON`; distinct-aware `X-Total-Count`; limit/offset only (v1) (parity)
- [x] Reserved param names (`select`, `order`, …) filterable only inside `and=(...)` — documented limitation (v1) (new)
- [ ] Range operators (`&&`, `@>` element, `<<`, `>>`) (v1.x, alongside structured range types) (parity)

## Pagination

- [x] `limit` (default and cap = `server.max_page_size`, default 100; larger → 400 `page_size_exceeded`) and `offset` (v1) (parity)
- [x] Keyset `after=` / `before=` cursors = base64url(JSON `["schema.table", pk…]`), decoded structurally, PK-typed (v1) (parity)
- [x] `before` = page immediately preceding the cursor (reversed scan, re-reversed) (v1) (parity)
- [x] Keyset requires a PK; views/PK-less → 400 `keyset_unsupported`; `offset` + `before` → 400 (v1) (new)
- [x] Deleted anchor row → empty page under non-PK order; PK-only orders keep paging from the cursor's key values (v1) (parity)
- [x] `count=true` → `X-Total-Count` via a `count(*)` subquery in the same statement, distinct-aware; off by default (v1) (parity)
- [x] `X-Next-Cursor` / `X-Prev-Cursor` headers when a next/prev page exists (v1) (new)
- [x] `Link: <…>; rel="next", <…>; rel="prev"` built from the request URL (`after=`/`before=`, or `offset=` for offset-only resources) (v1) (new)
- [x] Cost limit `server.max_cost` (1 + Σ mult × child; `@costMultiplier` lowers) → 400 `cost_exceeded` (v1) (parity)
- [ ] `Content-Range` / `Range` header pagination (PostgREST style) (stretch) (new)

## Mutations

- [x] `POST {base}/{table}` object → `INSERT` → `201` + row + `Location` (single-column PK) (v1) (parity)
- [x] `POST` array → bulk insert (one `INSERT … VALUES (…),(…)`, column union, missing → `DEFAULT`) → `201` + array (v1) (parity)
- [x] `POST {}` → `DEFAULT VALUES` (v1) (new)
- [x] `PATCH {base}/{table}/{pk}` and `…/by/{cols}/{vals}` → `200` + row; `404` (v1) (parity)
- [x] `DELETE {base}/{table}/{pk}` and `…/by/…` → `200` + deleted row (`204` with `Prefer: return=minimal`); `404` (v1) (parity)
- [x] `PUT {base}/{table}` upsert `ON CONFLICT (<pk>) DO UPDATE SET c = EXCLUDED.c`; `?on_conflict=cols` picks a unique constraint; single object `201`/`200` via `xmax = 0`, array → `200` (v1) (parity)
- [x] `If-Match` on `PATCH`/`DELETE` — two-CTE atomic check → `412` `etag_mismatch` / `404` (v1) (new)
- [x] `Prefer: return=minimal` → `204` (or `201` + `Location` on create) (v1) (new)
- [x] `select=` and embeds on mutation responses; `RETURNING` lists only referenced columns (v1) (parity)
- [x] Body validation: unknown key → 400 `invalid_body`; generated/identity → 400 `column_readonly`; required = `NOT NULL && !HasDefault` on create; PATCH all optional (v1) (parity)
- [x] Composite-typed columns accept nested objects (and arrays of them) (v1) (parity)
- [x] Privilege-gated routes (no INSERT grant → no `POST` → `405` + `Allow`) (v1) (parity)
- [x] Views / matviews → write methods absent → `405` + `Allow` (v1) (new)
- [x] `server.read_only` → every write `405` `read_only`; runtime-toggleable `App.SetReadOnly` (v1) (parity)
- [x] Not-found update/delete → `404`, transaction rolled back (v1) (parity)
- [x] Nested mutations via `nested-mutations` plugin on `POST` create: forward `{"create":…}` **or** `{"connect":…}`, backward `{"create":[…]}`, depth ≤ `max_depth` (3), forced transaction, multi-CTE single statement, visibility CTE for response embeds (v1) (parity)
- [ ] `PATCH|DELETE {base}/{table}` bulk-by-filter (`405` in v1) (v1.x) (parity)
- [ ] Nested `connect` on reverse relations and nested update/delete/disconnect (v1.x) (parity)
- [ ] `PUT {base}/{table}/{pk}` single-row upsert by path key (v1.x) (new)

## RPC & computed columns

- [x] `GET {base}/rpc/{fn}?arg=value` for stable/immutable functions (v1) (parity)
- [x] `POST {base}/rpc/{fn}` JSON body `{arg: value}` for any volatility; volatile via `GET` → `405` `Allow: POST` (v1) (parity)
- [x] Volatile → mutation semantics (tx, read-only `405`) (v1) (parity)
- [x] Args by name; missing → SQL `NULL`; unknown → 400; coerced by pg type; enum labels verbatim; spatial args GeoJSON/WKT (v1) (parity)
- [x] Scalar returns → JSON value `200` (via `to_jsonb`) (v1) (parity)
- [x] `void` → `204` (v1) (new)
- [x] Single row (table/composite) → object `200` (`null` when the function returns NULL) (v1) (parity)
- [x] `SETOF <table>` → array with full `select=` / filters / `order=` / `limit` / `offset` / `count=true` (offset paging only; `Accept: text/csv` supported) (v1) (parity — filters/order/count are a superset)
- [x] `SETOF <scalar>` → array (v1) (parity)
- [x] `rls.auth.jwt_type` composite return → minted HS256 JWT string (pdbcore/jwtmint) (v1) (parity)
- [x] EXECUTE-privilege gating; extension-owned / unnamed / OUT/INOUT (`record`) / trigger / polymorphic returns excluded (VARIADIC undetectable in the catalog) (v1) (parity)
- [x] Overloads / duplicate route names → keep-first + warning (v1) (parity)
- [x] Computed columns: stable/immutable row-type functions → `select=` fields, key = function name minus `<table>_` prefix, `@name` override (via the inflector; smart-comments plugin M5), real column wins on collision (v1) (parity)
- [x] Set-returning computed columns → array fields (v1) (parity)
- [x] `schema.functions: false` disables RPC + computed columns (v1) (parity)
- [ ] Computed-column extra args `field(k=v)` (grammar reserved) (v1.x) (parity)
- [ ] Functions with table-valued arguments or polymorphic types (v1.x) (parity)

## Errors

- [x] RFC 9457 `application/problem+json` (`type`, `title`, `status`, `detail`, `instance`) (v1) (new)
- [x] Stable `code` extension: `pg:<SQLSTATE>` or pdbr code (`not_found`, `invalid_filter`, `unknown_column`, `column_not_filterable`, `column_not_orderable`, `page_size_exceeded`, `keyset_unsupported`, `cost_exceeded`, `embed_depth_exceeded`, `ambiguous_relation`, `invalid_body`, `column_readonly`, `read_only`, `etag_mismatch`, `not_acceptable`, `unsupported_media_type`, `role_not_allowed`, `invalid_token`) (v1) (new)
- [x] `errors.detail: dev` adds `pg` object `{sqlstate, message, detail, hint, table, constraint}` (v1) (parity)
- [x] `errors.detail: dev|prod|strict` message policy (22*/23* pass through in prod; everything generic in strict) (v1) (parity)
- [x] Status mapping: 400 grammar/body/22*/23502/23514/42*, 401 credentials, 403 `42501`/role not allowed, 404 route/row, 405 + `Allow`, 406, 409 `23505`/`23503`/`23P01`/serialization exhausted, 412, 413, 415, 500 other, 503 `53*`/`08*`, 504 `57014`/request timeout (v1) (new)

## RLS & auth

- [x] On by default; per-request `SET LOCAL ROLE` (never plain `SET`) (v1) (parity)
- [x] Claims via transaction-scoped `set_config('pdbq.claims.*', …)` — prefix kept for policy compatibility with pdbq (v1) (parity)
- [x] JWT claim source (HS256/384/512, issuer/audience checks) (v1) (parity)
- [x] JWKS / RS256/384/512 + ES256/384/512, kid-matched, rotation-aware cache (`jwks_url`, `jwks_cache_ttl`) (v1) (parity)
- [x] Trusted-header claim source (`X-Pdbr-Claim-*`, `trusted_proxies`) (v1) (parity)
- [x] Anonymous role for unauthenticated requests; invalid credential → `401` (v1) (parity)
- [x] Role claim configurable; default role fallback (v1) (parity)
- [x] Role name validation before interpolation into `SET ROLE` (v1) (parity)
- [x] `rls.allowed_roles` allowlist; disallowed → `403` `role_not_allowed` (v1) (parity)
- [x] `rls.enabled: false` escape hatch with loud startup warning; warnings for headers without trusted proxies and empty `allowed_roles` (v1) (parity)
- [x] Claims/role exposed to plugins via `exec.Operation` and request context (v1) (parity)
- [x] `pdbr request` forces `rls.auth.mode=none` (v1) (parity)

## Transactions & execution

- [x] Every mutation in a transaction by default (`transactions.mutations`) (v1) (parity)
- [x] Reads transaction-free unless RLS role/claims require `SET LOCAL` (v1) (parity)
- [x] Plugins can force a transaction (`op.ForceTx`) (v1) (parity)
- [x] Isolation level config (read_committed / repeatable_read / serializable) (v1) (parity)
- [x] Statement timeout per connection (`database.statement_timeout`) (v1) (parity)
- [x] Automatic retry on `40001`/`40P01` (`transactions.max_retries`; whole request re-run, no backoff); exhausted → `409` (v1) (parity)
- [x] One statement per request; zero rows on single-row path → `404` (v1) (parity)
- [x] Wrapper-object unwrapping (`rows/total`, `etag/row`, `found/row`, `row/inserted`) in exec (v1) (new)
- [ ] Savepoints for partial mutation recovery (v1.x) (parity)

## Server & protocol

- [x] `server.base_path` (default `/api`; `""` = root) (v1) (new)
- [x] `GET /healthz` / `/readyz` outside base_path (v1) (parity)
- [x] `GET {base}/openapi.json` (`server.openapi`, default true) (v1) (new)
- [x] Content negotiation: JSON default; `text/csv`; `415` non-JSON body; `406` unsatisfiable `Accept` (v1) (new)
- [x] CORS configuration (`server.cors_origins`: exact allowlist or `*`) + preflight (v1) (parity)
- [x] Response compression (gzip, opt-in via `server.compression`) (v1) (parity)
- [x] Request timeout → `504` problem; max body size → `413` (v1) (parity)
- [x] Read-only mode (`server.read_only`, runtime toggle) (v1) (parity)
- [x] Atomic runtime hot-swap (in-flight requests finish on the old `api.Built`) (v1) (parity)
- [x] No embedded UI, no CDN assets (bring your own OpenAPI tooling) — by decision (v1) (new)
- [ ] `/metrics` (Prometheus) endpoint (v1.x) (parity)
- [ ] `Content-Range` / `Range` request headers (stretch) (new)

## OpenAPI 3.1

- [x] `info` (title `pdbr`, version = binary version), `servers: [{url: base_path}]` (v1) (new)
- [x] `tags`: one per resource (`users`, `audit.users`) + `rpc` / `<schema>.rpc`; operations tagged by resource, not schema (v1) (new)
- [x] `components.schemas`: one per resource (`users` / `audit.users`), `<name>.insert`, `<name>.update`, `<name>.upsert` (v1) (new)
- [x] Nested-input schema variants when `nested-mutations` is on (v1) (new) — M5 publishes `Surface.NestedKeys` + `<name>.connect` schemas; M6 renders the `.insert` properties (forward `create`/`connect` exactly-one, backward `create` list, FK columns optional)
- [x] One schema per enum and per composite; `GeoJSON`; `Problem` (v1) (new) — `GeoJSON` (+ spatial columns `$ref`, read-only pseudo-column properties, `oneOf [GeoJSON, string]` inputs) by the `postgis` plugin; enum/composite/`Problem` core
- [x] Type map per "Type mapping" section; nullable via `type: [T, "null"]`; `readOnly` for generated columns; `deprecated: true` + `x-deprecation-reason` from `@deprecated`; descriptions from pg comments (tags stripped) (v1) (new)
- [x] `paths`: list path (GET/HEAD/POST/PUT) with all query params — one param per filterable column with op vocabulary in its description, plus `select`, `order`, `limit`, `offset`, `after`, `before`, `count`, `distinct`, `and`/`or`/`not.and`/`not.or`, `near` (v1) (new)
- [x] `/{table}/{pk}` (GET/HEAD/PATCH/DELETE) and one concrete path per unique constraint (`/users/by/email/{email}`) (v1) (new)
- [x] RPC paths (GET/POST per volatility) with arg params / request body (v1) (new)
- [x] Responses reference `Problem` for 4xx/5xx; headers `X-Total-Count`, `X-Next-Cursor`, `X-Prev-Cursor`, `Link`, `ETag`, `Location`, `Allow` documented (v1) (new)
- [x] `securitySchemes.bearerAuth` when `rls.auth.mode=jwt` (v1) (new)
- [x] Tags per schema (v1) (new)
- [x] Vendor extension `x-pdbr-filter-ops` on column params (v1) (new)
- [x] Plugins add their surface via `APIHook` (postgis params, relation-filter prose, nested-input schemas) (v1) (new)
- [x] Deterministic output; golden test; validity checked in e2e with a test-only validator (v1) (new)
- [x] `pdbr schema print [--yaml]` emits the same document (v1) (new)
- [ ] OpenAPI-driven schema contracts (per-audience variants) (v2) (parity)

## Schema cache & watch mode

- [x] `pdbr schema dump` — versioned, hashed, gzipped catalog snapshot (pdbcore/cache) (v1) (parity)
- [x] `serve --schema.cache_path` — boot without touching pg_catalog (v1) (parity)
- [x] `pdbr schema check` — CI drift gate (non-zero exit on drift) with per-object diff detail (v1) (parity)
- [x] Format-version and corruption rejection on load (v1) (parity)
- [x] Cache files interchangeable with pdbq (v1) (new)
- [x] Watch mode: DDL event trigger + `LISTEN/NOTIFY` re-introspection (`watch.channel` `pdbr_ddl`) (v1) (parity)
- [x] Poll-hash fallback when event triggers can't be installed (v1) (parity)
- [x] Watch + cache combination rejected at config validation (v1) (parity)

## CLI & config

- [x] `pdbr serve|request|schema|config|plugins|completion` subcommands, `--version` (v1) (parity)
- [x] `pdbr request METHOD PATH [--body @file|-|'{json}'] [--header 'K: V']… [--print-sql] [--accept text/csv] [-i]`; base_path optional in PATH; exit `0` 2xx/3xx, `1` transport/config, `2` 4xx/5xx (v1) (parity)
- [x] `--print-sql` prints golden-format SQL without executing; works with `--schema.cache_path` and no DB (v1) (parity)
- [x] Config layering: flags > env (`PDBR_*`, `__` escapes underscores) > YAML > defaults (v1) (parity)
- [x] `pdbr config example` — annotated reference generated from struct tags (committed at `examples/pdbr.example.yaml`, can't drift) (v1) (parity)
- [x] `pdbr config init|validate` (v1) (parity)
- [x] `pdbr plugins list` with hook surfaces (v1) (parity)
- [x] `pdbr schema print [--yaml]` (OpenAPI) (v1) (new)
- [x] Shell completions (`bash|zsh|fish|powershell`, fixed-vocabulary flag values) (v1) (parity)
- [x] Validation: pdbq's 16 checks minus dropped keys, plus `server.base_path` shape and `server.max_embed_depth ≥ 1` (v1) (parity)

## Plugin system

- [x] Compile-time plugins; ordered registry (priority + registration order) (v1) (parity)
- [x] `CatalogHook` — transform the introspected catalog (pdbcore) (v1) (parity)
- [x] `InflectionHook` — override any generated name/key, middleware-chained (pdbcore types, pdbr kinds) (v1) (parity)
- [x] `APIHook` — mutate the `api.Builder` before `Build()` (routes, params, schemas) (v1) (new)
- [x] `CompileHook` — wrap SQL generation per request (v1) (parity)
- [x] `RequestHook` — before/after request, context + tx control (v1) (parity)
- [x] Enable/disable/configure per plugin via `plugins.*` config (v1) (parity)
- [x] Library embedding: `pdbr.New(cfg, pdbr.WithPlugins(...), pdbr.WithLogger(...))`, `App.Handler(ctx)` (v1) (parity)
- [x] Collision detection with warnings at API build (v1) (parity)
- [x] Built-in: `smart-comments` (50) — catalog via pdbcore + inflection + APIHook (route/method/param removal, deprecation, description stripping) — see [smart-comments.md](smart-comments.md) (v1) (parity)
- [x] Built-in: `advanced-filters` (150) — relation filters/existence, related order, computed filter/order; settings `relations`, `computed`, `relation_order`, `relations_opt_in`, `computed_opt_in`; `@omit filter|order` / `@filterable` read from the catalog (v1) (parity)
- [x] Built-in: `postgis` (160) — spatial filter params, pseudo-columns, projection args, distance order + `near`; settings `filters`, `derived`, `ordering`, `projection` (v1) (parity) — OpenAPI query params (`near`, spatial `x-pdbr-filter-ops` from `Built.Extra.FilterOps`, `oneOf [GeoJSON, string]` inputs from `Extra.InputTypes`) rendered by the core generator
- [x] Built-in: `nested-mutations` (200) — APIHook input schemas, CompileHook multi-CTE, RequestHook `ForceTx`; settings `force_transactions`, `max_depth` (v1) (parity)
- [ ] Out-of-process plugins (go-plugin gRPC or wasm) (v2) (parity)

## Ops & delivery

- [x] Multi-stage Dockerfile (`golang:1.26-alpine` → distroless nonroot, `ENTRYPOINT ["/pdbr"] CMD ["serve"]`) (v1) (parity)
- [x] Compose dev stack (`postgis/postgis:16-3.4` + fixture + watch mode, no host ports, gitignored `compose.override.yaml`) and `compose.test.yaml` (v1) (parity)
- [x] Makefile: `build dev test test-e2e bench fuzz lint example-config release docker-build docker-push compose-up compose-down` (v1) (parity)
- [x] CI: `lint-test` (golangci-lint + `-race` unit/golden), `e2e` (postgis service + fixture), `docker` (v1) (parity)
- [x] Release workflow on `v*`: 5-platform cross-compile, checksums, GitHub release, `ghcr.io/suprbdev/pdbr` + `docker.io/suprbdev/pdbr` multi-arch, `act`-aware (v1) (parity)
- [x] Golden-file compiler tests (`.http` → `.sql`, `-update` regeneration) (v1) (parity)
- [x] Fuzz: `FuzzFilter`, `FuzzSpatial`, `FuzzParse` (v1) (parity)
- [x] Benchmarks: `BenchmarkAPIBuild`, `BenchmarkCompile/{single_row,embeds,list_filters,keyset,rpc}`, `BenchmarkOpenAPI` (v1) (parity)
- [x] E2E suite: reads, embeds, filters, keyset walk (incl. deleted anchor), distinct, ETag/If-Match/304, CSV, mutations + rollback atomicity, upsert, nested mutation, retry, JWT mint, RLS matrix, smart comments, OpenAPI validity, postgis, plugin-disabled surfaces, read-only 405, errors table, watch mode (trigger + poll fallback), schema cache (offline boot, dump/check drift, pdbq interop) (v1) (parity)
- [x] `.golangci.yaml` committed from day one (v1) (new)
- [ ] Load benchmarks: pdbr + PostgREST lane in [pg-vs-pdbq](https://github.com/suprbdev/pg-vs-pdbq) — tracked in that repo, not here (v1.x) (parity)

## Dropped from pdbq (GraphQL-only, not applicable)

- GraphiQL / any embedded UI
- SDL export (`/schema.graphql`)
- APQ / persisted queries / `persisted_only`
- GraphQL introspection queries (`__schema`, `__type`) and `server.disable_introspection`
- Depth limit `server.max_depth` (replaced by `server.max_embed_depth`)
- Fragments, field aliases via GraphQL syntax, directives (`@skip`/`@include`) — aliases become `alias:field`
- Subscriptions / live queries
- `nodeId` / `node()` global object identification
- `clientMutationId`
- Relay connections / `edges` / `pageInfo` / `totalCount` — replaced by JSON arrays + `X-Total-Count`, `X-Next-Cursor`, `X-Prev-Cursor`, `Link` headers
- `simple-names` plugin (raw pg identifiers everywhere)
- `transactions.per_request` (one operation per request by construction)
- Multiple root fields per operation
