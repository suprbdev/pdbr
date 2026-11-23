# Feature support

Checklist of PostgreSQL and REST features pdbr supports. **Unticked rows are the backlog.** Checked = implemented and tested; unchecked = planned or explicitly deferred. Each item is tagged `(parity)` when it mirrors a pdbq feature or `(new)` when it exists only because of REST. Use this as the reference when adding features: tick the box in the same PR that ships it. See [architecture.md](architecture.md) for how the pieces fit.

## Introspection (pg_catalog — from pdbcore)

- [x] Tables (including partitioned tables) (parity)
- [x] Partitions hidden by default (`Table.IsPartition`; per-partition constraint clones no longer duplicate relations), `schema.expose_partitions` to expose them (parity)
- [x] Views (read-only) (parity)
- [x] Materialized views (read-only) (parity)
- [x] Columns: type, nullability, defaults, identity/generated detection (parity)
- [x] Primary keys (parity)
- [x] Unique constraints (parity)
- [x] Foreign keys (including multi-column) (parity)
- [x] Indexes (name, columns, uniqueness, method) — drives the filter policy (parity)
- [x] Enums (parity)
- [x] Functions (args, return type, set-returning, volatility) (parity)
- [x] Comments on tables, columns, enums, functions (exposed as OpenAPI descriptions), and constraints (smart comments) (parity)
- [x] RLS-enabled status per table (parity)
- [x] Granted privileges per table (SELECT/INSERT/UPDATE/DELETE gate route generation) (parity)
- [x] Schema allowlist (`schema.schemas`) (parity)
- [x] Composite types read into the catalog (parity)
- [x] Composite-type columns mapped to OpenAPI object schemas (nested objects in bodies; composite arrays included) (parity)
- [x] `CatalogFormatVersion` shared with pdbq; cache files interchangeable (new)
- [x] Domains surfaced as named schemas (`server.openapi_domains`, on by default: one component per domain over a built-in type, `pattern`/bounds from simple `CHECK`s; see [openapi.md](openapi.md#domains)) (parity)
- [x] Function argument default values / variadic args (`FuncArg.HasDefault`, `Function.Variadic`) (parity)
- [x] Functions with `OUT`/`INOUT` parameters / `RETURNS TABLE` (`Function.ReturnColumns`) (parity)
- [ ] Anonymous `record` returns (no output parameters: no shape to describe) (parity)

## Type mapping (Postgres → JSON/OpenAPI)

- [x] `int2`/`int4` → `integer` (`format: int32`) (parity)
- [x] `int8` → `string` (string-serialized, no precision loss; documented) (parity)
- [x] `float4`/`float8` → `number` (parity)
- [x] `numeric`/`money` → `string` (string-serialized) (parity)
- [x] `bool` → `boolean` (parity)
- [x] `text`/`varchar`/`char`/`citext`/`name` → `string` (parity)
- [x] `uuid` → `string` `format: uuid` (parity)
- [x] `json`/`jsonb` → `{}` (any) (parity)
- [x] `timestamp`/`timestamptz` → `string` `format: date-time` (parity)
- [x] `date` → `format: date`, `time`/`timetz` → `format: time` (parity)
- [x] `bytea` → `string` `contentEncoding: base64` (parity)
- [x] Arrays of any mapped type → `array` (parity)
- [x] Enums → `enum` with pg labels verbatim (round-tripped on input and output) (new)
- [x] Nullable columns → `type: [T, "null"]` (new)
- [x] `int8`/`numeric` accepted as JSON number **or** string on input (new)
- [x] `inet`/`cidr`/`macaddr`, `interval`, ranges, `ltree`, `tsvector` → `string` fallback (parity)
- [ ] `interval` as structured schema (parity)
- [x] Range and multirange types: literal text out (PostgREST parity), literal or structured `{start, end, startInclusive, endInclusive, isEmpty}` in, `oneOf` input schema — see [contrib](contrib.md#input-post--put--patch-bodies) (parity)
- [x] `hstore` → JSON object of string → string|null in and out (`type: object`), filter ops `hk hk_all hk_any cs cd` — see [contrib](contrib.md#hstore-pdbr) (parity)
- [x] `ltree` / `ltree[]` operators (`cs cd ancestor descendant ancestor_any descendant_any lquery lquery_any ltxtquery`) (parity)
- [x] Full-text search: `fts plfts phfts wfts` with optional `(config)` on `tsvector` and text-like columns (parity)
- [x] Extension types and operators gated on `pg_extension` (and `pg_trgm` version), schema-qualified when the extension schema is off the `search_path`; citext comparisons stay case-insensitive there (new)
- [x] PostGIS `geometry`/`geography` → `GeoJSON` schema (GeoJSON out; GeoJSON or WKT/EWKT in), with spatial filters, derived accessors, projection arguments and distance ordering — see [PostGIS](postgis.md) (parity)

## Reads

- [x] `GET {base}/{table}` → JSON array `200` (parity)
- [x] `HEAD {base}/{table}` — same headers (incl. `X-Total-Count`), no body (new)
- [x] `GET {base}/{table}/{pk}` → object `200`; none → `404` problem; composite PK comma-separated in PK column order, percent-encoded (parity)
- [x] `GET {base}/{table}/by/{cols}/{vals}` — lookup per unique constraint, `cols` order-insensitive, `vals` positional (parity)
- [x] `GET {base}/{schema}/{table}…` schema-qualified forms; bare alias only for `schema.schemas[0]` (new)
- [x] Bare aliases suppressed with warning on reserved-segment collision (`rpc`, `openapi.json`, `healthz`, `readyz`, `by`) (new)
- [x] `select=` projection: columns, `*`, `alias:field`, computed columns, PostGIS pseudo-columns (parity)
- [x] PostGIS accessors `_x`, `_y`, `_z`, `_m`, `_num_points`, `_num_geometries`, `_start_point`, `_end_point`, `_point_on_surface`, `_wkt`, `_ewkt`, `_is_empty`, `_is_simple`, `_xmin`/`_ymin`/`_xmax`/`_ymax`; type-specific ones generated only where the column's typmod allows a non-null value (`introspect.Column.Spatial`) — see [PostGIS](postgis.md#derived-accessors-pseudo-columns) (parity)
- [x] Embeds `rel(select)` compiled to `LEFT JOIN LATERAL` — forward → object or `null`, backward → array (parity)
- [x] Arbitrarily nested embeds up to `server.max_embed_depth` (default 5) → else 400 `embed_depth_exceeded` (parity)
- [x] Relation keys = table names; `!fk_name` / `!column` hints for ambiguous relations; unhinted ambiguity → 400 `ambiguous_relation` listing candidates (new)
- [x] `!inner` hint — embed filters also restrict the parent (new)
- [x] Per-embed params on backward embeds: `posts.order=`, `posts.limit=`, `posts.offset=`, `posts.<col>=op.v` (parity)
- [x] Forward-embed filters null out the embed unless `!inner` (new)
- [x] Field aliases `alias:field` and embed aliases `author:users(name)` (parity)
- [x] JSON paths `col->key`, `col->0`, `col->>key` in `select=` (response key = last key), filters (`->>` text, `->` jsonb operands) and `order=`, on json/jsonb, array and (select only) composite columns; keys bound as parameters — see [api.md](api.md#json-paths) (parity)
- [x] Spread embeds `...rel(cols)`: to-one values hoisted into the row, to-many correlated arrays per item (PostgREST 12), nested spreads, `!inner`, per-embed params, CSV — see [api.md](api.md#spread-embeds) (parity)
- [x] Column casts `col::type`, `col->key::type` in `select=` (allowlisted built-in types and enums) — see [api.md](api.md#casts) (parity)
- [x] `select=` applies to mutation responses (compiled against `__mut`) (parity)
- [x] Single-row weak `ETag: W/"<md5>"` computed in SQL over the exposed columns; `If-None-Match` → `304`; `server.etags` toggle (new)
- [x] `Accept: text/csv` on lists and set-returning RPCs → RFC 4180 CSV, header row, arrays/json/composites as JSON text, NULL → empty; formula cells escaped (`server.csv_escape_formulas`); embeds → `406`; `server.csv` toggle (new)
- [x] `Prefer: return=representation|minimal|headers-only` (new)
- [x] Aggregate functions in `select=` (PostgREST v12): `count()`, `col.count()`, `sum`, `avg`, `max`, `min`, aliases, input/output casts, implicit `GROUP BY` (columns, casts, JSON paths, computed fields, to-one spread items — spread grouping), aggregates inside to-many embeds and to-one spreads, `order=` on grouping columns, offset paging and group totals, CSV; `server.aggregates` (off → 400 `aggregates_disabled`) — see [aggregates.md](aggregates.md) (parity)
- [x] Spatial aggregates `col.extent()`, `col.union()`, `col.collect()`, `col.centroid()` → GeoJSON (postgis plugin) (new)
- [x] `Prefer` tokens `resolution=`, `missing=` on writes (new)
- [x] Opt-in partial success for bulk `POST`/`PUT`: `Prefer: partial=items` (needs `server.partial_bulk_writes`) runs each element in its own savepoint and answers a per-element report, `207` when one failed — see [Mutations](mutations.md#partial-success-for-bulk-writes)
- [x] `Prefer: tx=commit|rollback` on writes, gated by `server.tx_end` (PostgREST `db-tx-end`: `commit`, `commit-allow-override`, `rollback`, `rollback-allow-override`); the rolled-back request still answers with its result (parity)
- [x] `Prefer: timezone=<IANA name>` sets `timezone` for the request's transaction, validated against `pg_timezone_names` (cached); echoed in `Preference-Applied` (parity)
- [x] `columns=` query parameter on `POST`/`PATCH`/`PUT`: only the listed body keys are used, others ignored; a listed key an element omits takes `DEFAULT`; bulk bodies may be heterogeneous (parity)

## Filtering & ordering

- [x] `{col}=[not.]{op}.{value}` grammar; unknown column → 400 `unknown_column`; unknown op → 400 `invalid_filter` (new)
- [x] Scalar operators: `eq neq gt gte lt lte in is` (`is.null|true|false`) (parity)
- [x] `in.(a,b,"c,d")` / `not.in.(…)` → `= ANY($n)` / `<> ALL($n)` (parity)
- [x] Text operators: `like ilike sw ew` (`*` → `%`; `sw`/`ew` operands escaped) (parity)
- [x] Regular expressions `match` (`~`) and `imatch` (`~*`), citext through its own operators (parity)
- [x] `any`/`all` modifiers on `eq gt gte lt lte like ilike match imatch` (`col=like(any).{a*,b*}` → `LIKE ANY($n)`) (parity)
- [x] Array operators: `cs cd ov` (`{a,b}` literal) (parity)
- [x] `jsonb` operators: `cs cd hk jpe jpm` (parity)
- [x] Enum filtering with pg labels verbatim (parity)
- [x] Logic: `and=(…)`, `or=(…)`, `not.and=`, `not.or=`, arbitrarily nested; quoted values with backslash escapes (parity)
- [x] Values coerced by column type at parse time and passed as `$n` params (parity)
- [x] Indexed-only policy (default): only indexed/PK/unique columns filterable and orderable → else 400 `column_not_filterable` / `column_not_orderable` (parity)
- [x] Per-table column allowlist override (`filters.allow_columns`) (parity)
- [x] Global policy switch (`filters.indexed_only: false`) (parity)
- [x] `@filterable` / `@sortable` smart-comment opt-in (parity)
- [x] Filters on embedded resources respect the embedded table's own policy (parity)
- [x] `order=col.asc|desc[.nullsfirst|nullslast],…`; NULLS LAST default both directions; PK tiebreaker always appended (parity)
- [x] `order=` by JSON paths (`order=settings->>theme.desc`), keyset-capable (parity)
- [x] Filtering across relations (`posts.title=eq.x`, `posts!none.`, `posts!every.`, `users.name=eq.x`) — `advanced-filters` plugin, EXISTS subqueries; one level deep (parity)
- [x] Relation existence (`posts=exists.true|false`, `users=exists.false`) (parity)
- [x] Filtering/ordering on computed columns (`post_count=gt.3`, `order=post_count.desc`) — `advanced-filters` plugin (parity)
- [x] Related-column ordering `order=users(name).asc` (forward: correlated subquery; backward: MIN/MAX) — `advanced-filters` plugin, policy of the related table applies (parity)
- [x] Spatial filter ops (`st_intersects` … `st_equals`, `st_dwithin`, `st_beyond`, `bbox_*`) — `postgis` plugin (parity)
- [x] Spatial values parsed for their column's typmod SRID (`pdbcore/postgis.ParseFor`): bare WKT tagged with it, GeoJSON reprojected to it on geometry columns, EWKT kept — filters, `near=`, bodies — see [PostGIS](postgis.md) (parity)
- [x] Directional/equality bounding-box ops (`bbox_leftof`, `bbox_rightof`, `bbox_below`, `bbox_above`, `bbox_overlapsor{leftof,rightof,below,above}`, `bbox_equals`, `bbox_intersectsnd`) and `st_exactlyequals`, `st_orderingequals`, `st_intersects3d`, geometry operand — `postgis` plugin (parity)
- [x] Distance ordering `order=location.dist.asc&near=…`; missing `near` → 400 (parity)
- [x] `distinct=col1,col2` → `SELECT DISTINCT ON`; distinct-aware `X-Total-Count`; limit/offset only (parity)
- [x] Reserved param names (`select`, `order`, …) filterable only inside `and=(...)` — documented limitation (new)
- [x] Range operators `ov cs cd sl sr nxr nxl adj` (range literal or JSON object operand; `cs.<element>`) on ranges and multiranges (parity)
- [x] Custom range types (`CREATE TYPE … AS RANGE`) and their multiranges: range operators, structured input and `oneOf` input schema, schema-qualified constructors
- [x] `isdistinct` (`IS DISTINCT FROM`) on every comparable column (parity)
- [x] Trigram similarity `trgm_sim trgm_wsim trgm_swsim` (pg_trgm) (new)
- [x] inet/cidr containment `cs cd cs_strict cd_strict ov` (new)
- [x] Full-text relevance ordering `order=<col>.rank|rank_cd` against the column's fts filter; trigram distance ordering `order=<col>.trgm_dist|trgm_wdist|trgm_swdist&trgm=<text>` (new)
- [x] Indexed-only policy per contrib operator from pdbcore index hints (new)

## Pagination

- [x] `limit` (default and cap = `server.max_page_size`, default 100; larger → 400 `page_size_exceeded`) and `offset` (parity)
- [x] Keyset `after=` / `before=` cursors = base64url(JSON `["schema.table", pk…]`), decoded structurally, PK-typed (parity)
- [x] `before` = page immediately preceding the cursor (reversed scan, re-reversed) (parity)
- [x] Keyset requires a PK; views/PK-less → 400 `keyset_unsupported`; `offset` + `before` → 400 (new)
- [x] Deleted anchor row → empty page under non-PK order; PK-only orders keep paging from the cursor's key values (parity)
- [x] `count=true` → `X-Total-Count` via a `count(*)` subquery in the same statement, distinct-aware; off by default (parity)
- [x] `X-Next-Cursor` / `X-Prev-Cursor` headers when a next/prev page exists (new)
- [x] `Link: <…>; rel="next", <…>; rel="prev"` built from the request URL (`after=`/`before=`, or `offset=` for offset-only resources) (new)
- [x] Cost limit `server.max_cost` (1 + Σ mult × child; `@costMultiplier` lowers) → 400 `cost_exceeded` (parity)
- [x] `Prefer: count=exact|planned|estimated` (planned = `pg_class.reltuples`, summed over leaf partitions; estimated = exact up to `server.max_page_size` rows; filtered/distinct/RLS/view/function requests count exactly) and `Content-Range: 0-24/*|N` on list pages, alongside `X-Total-Count` (new)
- [x] `Range: <first>-<last>` / `<first>-` request header (`Range-Unit: items`), intersected with `limit`/`offset`; `206 Partial Content` for a counted partial page and `416 Range Not Satisfiable` with `Content-Range: */<total>` past the total (parity)

## Mutations

- [x] `POST {base}/{table}` object → `INSERT` → `201` + row + `Location` (single-column PK) (parity)
- [x] `POST` array → bulk insert (one `INSERT … VALUES (…),(…)`, column union, missing → `DEFAULT`) → `201` + array (parity)
- [x] `POST {}` → `DEFAULT VALUES` (new)
- [x] `PATCH {base}/{table}/{pk}` and `…/by/{cols}/{vals}` → `200` + row; `404` (parity)
- [x] `DELETE {base}/{table}/{pk}` and `…/by/…` → `200` + deleted row (`204` with `Prefer: return=minimal`); `404` (parity)
- [x] `PUT {base}/{table}` upsert `ON CONFLICT (<pk>) DO UPDATE SET c = EXCLUDED.c`; `?on_conflict=cols` picks a unique constraint; single object `201`/`200` via `xmax = 0`, array → `200` (parity)
- [x] `If-Match` on `PATCH`/`DELETE` — two-CTE atomic check → `412` `etag_mismatch` / `404` (new)
- [x] `Prefer: return=minimal` → `204` (or `201` + `Location` on create) (new)
- [x] `select=` and embeds on mutation responses; `RETURNING` lists only referenced columns (parity)
- [x] Mutation-response embeds back to the mutated table see it as it stands after the statement (created and updated rows included, deleted rows excluded) via a `__vis` visibility CTE, emitted only when an embed reads the table (parity)
- [x] Body validation: unknown key → 400 `invalid_body`; generated/identity → 400 `column_readonly`; required = `NOT NULL && !HasDefault` on create; PATCH all optional (parity)
- [x] Composite-typed columns accept nested objects (and arrays of them) (parity)
- [x] Privilege-gated routes (no INSERT grant → no `POST` → `405` + `Allow`) (parity)
- [x] Views / matviews → write methods absent → `405` + `Allow` (new)
- [x] `server.read_only` → every write `405` `read_only`; runtime-toggleable `App.SetReadOnly` (parity)
- [x] Not-found update/delete → `404`, transaction rolled back (parity)
- [x] Nested mutations via `nested-mutations` plugin on `POST` create: forward `{"create":…}` **or** `{"connect":…}`, backward `{"create":[…]}`, depth ≤ `max_depth` (3), forced transaction, multi-CTE single statement, visibility CTE for response embeds (parity)
- [x] `PATCH|DELETE {base}/{table}?<filters>` bulk-by-filter: read filter grammar and policy, `200` + array / `204`, unfiltered → `400 unfiltered_mutation` unless `server.allow_unfiltered_mutations` (parity)
- [x] `Prefer: handling=strict|lenient` (`400 invalid_preference`), `max-affected=N` on `PATCH`/`DELETE` (`400 max_affected_exceeded`, rolled back), `return=headers-only`, `Preference-Applied` (new)
- [x] Nested `connect` on reverse relations (`{"connect":[{<key>}]}`) and, on single-row `PATCH`, nested update/delete/disconnect both ways (forward `{"update":{…}}`/`{"delete":true}`/`{"disconnect":true}`, backward `update:[{"where","data"}]`/`delete:[…]`/`disconnect:[…]`); guards (`404`, whole statement rolled back), RLS-scoped, `@omit` per action, `max_operations` (50), visibility CTEs exclude replaced/deleted rows — see [mutations.md](mutations.md#nested-mutations-plugin-nested-mutations) (parity)
- [x] `PUT {base}/{table}/{pk}` and `…/by/{cols}/{vals}` single-row upsert by path key (body key columns optional, must match; writable keys only) (new)
- [x] `POST` + `Prefer: resolution=merge-duplicates|ignore-duplicates` with `?on_conflict=` (ignore returns only inserted rows); `Prefer: missing=default` accepted (parity)

## RPC & computed columns

- [x] `GET {base}/rpc/{fn}?arg=value` for stable/immutable functions (parity)
- [x] `POST {base}/rpc/{fn}` JSON body `{arg: value}` for any volatility; volatile via `GET` → `405` `Allow: POST` (parity)
- [x] Volatile → mutation semantics (tx, read-only `405`) (parity)
- [x] Args by name (named notation); omitted → the function's `DEFAULT` (none → `400 missing_argument`), `null` → SQL `NULL`; unknown → 400; coerced by pg type; enum labels verbatim; spatial args GeoJSON/WKT (parity)
- [x] `RETURNS TABLE` / `OUT`-parameter functions: objects keyed by the result columns; `SETOF record` paged like `SETOF <table>` with `select=`, filters, `order=`, `count`, CSV; OpenAPI `rpc.<fn>` schema (parity)
- [x] `VARIADIC` arguments: repeated query params on GET, JSON array in the body, called with `VARIADIC` (parity)
- [x] Computed columns whose extra arguments all have defaults (parity)
- [x] Scalar returns → JSON value `200` (via `to_json`) (parity)
- [x] `void` → `204` (new)
- [x] Single row (table/composite) → object `200` (`null` when the function returns NULL) (parity)
- [x] `SETOF <table>` → array with full `select=` / filters / `order=` / `limit` / `offset` / `count=true` (offset paging only; `Accept: text/csv` supported) (parity — filters/order/count are a superset)
- [x] `SETOF <scalar>` → array (parity)
- [x] `rls.auth.jwt_type` composite return → minted HS256 JWT string (pdbcore/jwtmint) (parity)
- [x] EXECUTE-privilege gating; extension-owned / unnamed / anonymous `record` / trigger / polymorphic returns excluded (parity)
- [x] Overloads / duplicate route names → keep-first + warning (parity)
- [x] Computed columns: stable/immutable row-type functions → `select=` fields, key = function name minus `<table>_` prefix, `@name` override (via the inflector; smart-comments plugin M5), real column wins on collision (parity)
- [x] Set-returning computed columns → array fields (parity)
- [x] `schema.functions: false` disables RPC + computed columns (parity)
- [x] Computed-column extra args `select=field(k=v,…)`: validated against the function's arguments, bound as parameters, `400 missing_argument` when a required one is absent; select only (filters/order need all-defaulted args); documented in OpenAPI (parity)
- [ ] Functions with table-valued arguments or polymorphic types (parity)

## Errors

- [x] RFC 9457 `application/problem+json` (`type`, `title`, `status`, `detail`, `instance`) (new)
- [x] Stable `code` extension: `pg:<SQLSTATE>` or pdbr code (`not_found`, `invalid_filter`, `unknown_column`, `column_not_filterable`, `column_not_orderable`, `page_size_exceeded`, `keyset_unsupported`, `cost_exceeded`, `embed_depth_exceeded`, `ambiguous_relation`, `invalid_body`, `column_readonly`, `read_only`, `etag_mismatch`, `not_acceptable`, `unsupported_media_type`, `role_not_allowed`, `invalid_token`) (new)
- [x] `errors.detail: dev` adds `pg` object `{sqlstate, message, detail, hint, table, constraint}` (parity)
- [x] `errors.detail: dev|prod|strict` message policy (22*/23* pass through in prod; everything generic in strict) (parity)
- [x] Status mapping: 400 grammar/body/22*/23502/23514/42*, 401 credentials, 403 `42501`/role not allowed, 404 route/row, 405 + `Allow`, 406, 409 `23505`/`23503`/`23P01`/serialization exhausted, 412, 413, 415, 500 other, 503 `53*`/`08*`, 504 `57014`/request timeout (new)
- [x] `Retry-After: 1` on every `503` problem (`unavailable`, `pg:08…`, `pg:53…`, `/readyz`) (new)
- [x] PostgreSQL 18 temporal keys: `WITHOUT OVERLAPS` keys back no single-row route, `ETag` or upsert target (`400 temporal_conflict_target`; `resolution=ignore-duplicates` skips overlaps via `ON CONFLICT ON CONSTRAINT`), `PERIOD` FK embeds/filters/order join on `&&` and embed forward as arrays, nested writes copy only the equality columns, `NOT ENFORCED` FK embeds documented nullable (new)
- [x] `23001` restrict violation (PostgreSQL 18+) → 409; PostGIS input errors (`XX000` mixed SRID / invalid geometry) and contrib input errors (`42601` tsquery/ltree syntax, `42704` unknown text search configuration) → 400 with a sanitized message (`invalid geometry` / passthrough; `invalid input value` in strict) (new)

## RLS & auth

- [x] On by default; per-request `SET LOCAL ROLE` (never plain `SET`) (parity)
- [x] Claims via transaction-scoped `set_config('pdbq.claims.*', …)` — prefix kept for policy compatibility with pdbq (parity)
- [x] JWT claim source (HS256/384/512, issuer/audience checks) (parity)
- [x] JWKS / RS256/384/512 + ES256/384/512, kid-matched, rotation-aware cache (`jwks_url`, `jwks_cache_ttl`) (parity)
- [x] Trusted-header claim source (`X-Pdbr-Claim-*`, `trusted_proxies`) (parity)
- [x] Anonymous role for unauthenticated requests; invalid credential → `401` (parity)
- [x] Role claim configurable; default role fallback (parity)
- [x] Role name validation before interpolation into `SET ROLE` (parity)
- [x] `rls.allowed_roles` allowlist; disallowed → `403` `role_not_allowed` (parity)
- [x] `rls.enabled: false` escape hatch with loud startup warning; warnings for headers without trusted proxies and empty `allowed_roles` (parity)
- [x] Claims/role exposed to plugins via `exec.Operation` and request context (parity)
- [x] `pdbr request` forces `rls.auth.mode=none` (parity)

## Transactions & execution

- [x] Every mutation in a transaction by default (`transactions.mutations`) (parity)
- [x] Reads transaction-free unless RLS role/claims require `SET LOCAL` (parity)
- [x] Plugins can force a transaction (`op.ForceTx`) (parity)
- [x] Isolation level config (read_committed / repeatable_read / serializable) (parity)
- [x] Statement timeout per connection (`database.statement_timeout`) (parity)
- [x] Automatic retry on `40001`/`40P01` (`transactions.max_retries`; whole request re-run, no backoff); exhausted → `409` (parity)
- [x] One statement per request; zero rows on single-row path → `404` (parity)
- [x] Wrapper-object unwrapping (`rows/total`, `etag/row`, `found/row`, `row/inserted`) in exec (new)

## Server & protocol

- [x] `server.base_path` (default `/api`; `""` = root) (new)
- [x] `GET /healthz` (liveness) / `/readyz` (bounded pool ping, `server.readyz_timeout`, `503 unavailable` when the database is unreachable) outside base_path (parity)
- [x] `GET {base}/openapi.json` (`server.openapi`, default true) (new)
- [x] Content negotiation: JSON default; `text/csv`; `415` non-JSON body; `406` unsatisfiable `Accept` (new)
- [x] CORS configuration (`server.cors_origins`: exact allowlist or `*`) + preflight (parity)
- [x] Response compression (gzip, opt-in via `server.compression`) (parity)
- [x] Request timeout → `504` problem; max body size → `413` (parity)
- [x] Read-only mode (`server.read_only`, runtime toggle) (parity)
- [x] Atomic runtime hot-swap (in-flight requests finish on the old `api.Built`) (parity)
- [x] No embedded UI, no CDN assets (bring your own OpenAPI tooling) — by decision (new)
- [x] `/metrics` (Prometheus) endpoint: HTTP, database, pool, schema-rebuild and compile metrics by fixed route class, cost-limit and aggregate counters, main or separate listener (`metrics.*`) — see [metrics.md](metrics.md) (parity)
- [x] `Content-Range` response header on list pages and bulk writes (new)
- [x] `Range` request header (see Pagination) (parity)

## OpenAPI 3.1

- [x] `info` (title `pdbr`, version = binary version), `servers: [{url: base_path}]` (new)
- [x] `tags`: one per resource (`users`, `audit.users`) + `rpc` / `<schema>.rpc`; operations tagged by resource, not schema (new)
- [x] `components.schemas`: one per resource (`users` / `audit.users`), `<name>.insert`, `<name>.update`, `<name>.upsert` (new)
- [x] Nested-input schema variants when `nested-mutations` is on (new) — the plugin publishes `Surface.NestedKeys`, `Surface.NestedSchema` and `<name>.connect` schemas; the core renders the `.insert` and `.update` properties (only the operations each key offers there; forward `create`/`connect` exactly-one on `.insert`, FK columns optional)
- [x] One schema per enum and per composite; `GeoJSON`; `Problem` (new) — `GeoJSON` (+ spatial columns `$ref`, read-only pseudo-column properties, `oneOf [GeoJSON, string]` inputs) by the `postgis` plugin; enum/composite/`Problem` core
- [x] Type map per "Type mapping" section; nullable via `type: [T, "null"]`; `readOnly` for generated columns; `deprecated: true` + `x-deprecation-reason` from `@deprecated`; descriptions from pg comments (tags stripped) (new)
- [x] `paths`: list path (GET/HEAD/POST/PUT) with all query params — one param per filterable column with op vocabulary in its description, plus `select`, `order`, `limit`, `offset`, `after`, `before`, `count`, `distinct`, `and`/`or`/`not.and`/`not.or`, `near` (new)
- [x] `/{table}/{pk}` (GET/HEAD/PATCH/DELETE) and one concrete path per unique constraint (`/users/by/email/{email}`) (new)
- [x] RPC paths (GET/POST per volatility) with arg params / request body (new)
- [x] Responses reference `Problem` for 4xx/5xx; headers `X-Total-Count`, `X-Next-Cursor`, `X-Prev-Cursor`, `Link`, `ETag`, `Location`, `Allow` documented (new)
- [x] `securitySchemes.bearerAuth` when `rls.auth.mode=jwt` (new)
- [x] Tags per schema (new)
- [x] Vendor extension `x-pdbr-filter-ops` on column params (new)
- [x] Plugins add their surface via `APIHook` (postgis params, relation-filter prose, nested-input schemas) (new)
- [x] Deterministic output; golden test; validity checked in e2e with a test-only validator (new)
- [x] `pdbr schema print [--yaml]` emits the same document (new)
- [x] Schema contracts: per-audience API variants from `@contract` / `@behavior ±contract:<name>` tags, each with its own base path and OpenAPI document (`schema print --contract`) (parity; [contracts.md](contracts.md))

## Schema cache & watch mode

- [x] `pdbr schema dump` — versioned, hashed, gzipped catalog snapshot (pdbcore/cache) (parity)
- [x] `serve --schema.cache_path` — boot without touching pg_catalog (parity)
- [x] `pdbr schema check` — CI drift gate (non-zero exit on drift) with per-object diff detail (parity)
- [x] Format-version and corruption rejection on load (parity)
- [x] Cache files interchangeable with pdbq (new)
- [x] Watch mode: DDL event trigger + `LISTEN/NOTIFY` re-introspection (`watch.channel` `pdbr_ddl`) (parity)
- [x] Poll-hash fallback when event triggers can't be installed (parity)
- [x] Watch + cache combination rejected at config validation (parity)

## CLI & config

- [x] `pdbr serve|request|schema|config|plugins|completion` subcommands, `--version` (parity)
- [x] `pdbr request METHOD PATH [--body @file|-|'{json}'] [--header 'K: V']… [--print-sql] [--accept text/csv] [-i]`; base_path optional in PATH; exit `0` 2xx/3xx, `1` transport/config, `2` 4xx/5xx (parity)
- [x] `--print-sql` prints golden-format SQL without executing; works with `--schema.cache_path` and no DB (parity)
- [x] Config layering: flags > env (`PDBR_*`, `__` escapes underscores) > YAML > defaults (parity)
- [x] `pdbr config example` — annotated reference generated from struct tags (committed at `examples/pdbr.example.yaml`, can't drift) (parity)
- [x] `pdbr config init|validate` (parity)
- [x] `pdbr plugins list` with hook surfaces (parity)
- [x] `pdbr schema print [--yaml]` (OpenAPI) (new)
- [x] Shell completions (`bash|zsh|fish|powershell`, fixed-vocabulary flag values) (parity)
- [x] Validation: pdbq's 16 checks minus dropped keys, plus `server.base_path` shape and `server.max_embed_depth ≥ 1` (parity)

## Plugin system

- [x] Compile-time plugins; ordered registry (priority + registration order) (parity)
- [x] `CatalogHook` — transform the introspected catalog (pdbcore) (parity)
- [x] `InflectionHook` — override any generated name/key, middleware-chained (pdbcore types, pdbr kinds) (parity)
- [x] `APIHook` — mutate the `api.Builder` before `Build()` (routes, params, schemas) (new)
- [x] `CompileHook` — wrap SQL generation per request (parity)
- [x] `RequestHook` — before/after request, context + tx control (parity)
- [x] Enable/disable/configure per plugin via `plugins.*` config (parity)
- [x] Library embedding: `pdbr.New(cfg, pdbr.WithPlugins(...), pdbr.WithLogger(...))`, `App.Handler(ctx)` (parity)
- [x] Collision detection with warnings at API build (parity)
- [x] Built-in: `smart-comments` (50) — catalog via pdbcore + inflection + APIHook (route/method/param removal, deprecation, description stripping) — see [smart-comments.md](smart-comments.md) (parity)
- [x] Built-in: `advanced-filters` (150) — relation filters/existence, related order, computed filter/order; settings `relations`, `computed`, `relation_order`, `relations_opt_in`, `computed_opt_in`; `@omit filter|order` / `@filterable` read from the catalog (parity)
- [x] Built-in: `postgis` (160) — spatial filter params, pseudo-columns, projection args, distance order + `near`; settings `filters`, `derived`, `ordering`, `projection` (parity) — OpenAPI query params (`near`, spatial `x-pdbr-filter-ops` from `Built.Extra.FilterOps`, `oneOf [GeoJSON, string]` inputs from `Extra.InputTypes`) rendered by the core generator
- [x] Built-in: `nested-mutations` (200) — APIHook input schemas, CompileHook multi-CTE, RequestHook `ForceTx`; settings `force_transactions`, `max_depth`, `max_operations` (parity)
- [ ] Out-of-process plugins (go-plugin gRPC or wasm) (parity)

## Ops & delivery

- [x] Multi-stage Dockerfile (`golang:1.26-alpine` → distroless nonroot, `ENTRYPOINT ["/pdbr"] CMD ["serve"]`) (parity)
- [x] Compose dev stack (`postgis/postgis:16-3.4` + fixture + watch mode, no host ports, gitignored `compose.override.yaml`) and `compose.test.yaml` (parity)
- [x] Makefile: `build dev test test-e2e bench fuzz lint example-config release docker-build docker-push compose-up compose-down` (parity)
- [x] CI: `lint-test` (golangci-lint + `-race` unit/golden), `e2e` (postgis service + fixture), `docker` (parity)
- [x] Release workflow on `v*`: 5-platform cross-compile, checksums, GitHub release, `ghcr.io/suprbdev/pdbr` + `docker.io/suprbdev/pdbr` multi-arch, `act`-aware (parity)
- [x] Golden-file compiler tests (`.http` → `.sql`, `-update` regeneration) (parity)
- [x] Fuzz: `FuzzFilter`, `FuzzSpatial`, `FuzzParse` (parity)
- [x] Benchmarks: `BenchmarkAPIBuild`, `BenchmarkCompile/{single_row,embeds,list_filters,keyset,rpc}`, `BenchmarkOpenAPI` (parity)
- [x] E2E suite: reads, embeds, filters, keyset walk (incl. deleted anchor), distinct, ETag/If-Match/304, CSV, mutations + rollback atomicity, upsert, nested mutation, retry, JWT mint, RLS matrix, smart comments, OpenAPI validity, postgis, plugin-disabled surfaces, read-only 405, errors table, watch mode (trigger + poll fallback), schema cache (offline boot, dump/check drift, pdbq interop) (parity)
- [x] `.golangci.yaml` committed from day one (new)
- [ ] Load benchmarks: pdbr + PostgREST lane in [pg-vs-pdbq](https://github.com/suprbdev/pg-vs-pdbq) — tracked in that repo, not here (parity)

## Differences from PostgREST

pdbr follows PostgREST's URL grammar and `Prefer` vocabulary where it can;
these are the deliberate departures (each documented in its topic page):

- **Default `return`.** PostgREST writes default to `return=minimal`; pdbr
  defaults to `representation`, and its `minimal` keeps `201` + `Location`
  on single-object creates, so `return=headers-only` is an alias of
  `minimal`.
- **Unfiltered bulk writes are refused** (`400 unfiltered_mutation`) unless
  `server.allow_unfiltered_mutations` is on; PostgREST leaves this to
  `pg-safeupdate`. Like `pg-safeupdate`, the guard checks presence, not
  selectivity. Bulk writes are not offered on collections hidden by
  `@omit read|all`, and `If-Match` on them is `400 if_match_unsupported`.
- **Bulk `PATCH`/`DELETE` take no `limit`/`order`** (PostgREST's limited
  update/delete) and answer `200` + array / `204` — never `404` — when
  nothing matches.
- **`max-affected` is checked after the one statement, inside its
  transaction** (exec compares the row count the statement reports and rolls
  back), not by a second query; it applies to `PATCH`/`DELETE` only, not to
  RPC results (PostgREST `PGRST128`).
- **`count=planned` reads `pg_class.reltuples`** instead of running
  `EXPLAIN` (a second statement); it and `count=estimated` (threshold
  `server.max_page_size`, PostgREST's `db-max-rows`) apply to unfiltered
  scans of tables without RLS only and fall back to `exact` otherwise
  (`Preference-Applied` says so).
- **Keyset pages carry no `Content-Range`** and stay `200`; a `Range` header
  cannot be combined with `after`/`before` (`400 keyset_unsupported`).
  `X-Total-Count` is kept next to `Content-Range`. `206`/`416`, the
  `Range` grammar and its intersection with `limit`/`offset` follow
  PostgREST; `HEAD` honours `Range` (PostgREST ignores it), and an explicit
  `limit=` above `server.max_page_size` stays `400` (PostgREST clamps it to
  `db-max-rows`) while a `Range` is clamped.
- **`PUT` on a single-row path is a partial upsert**: PostgREST requires
  every column in the body (`PUT /t?id=eq.1`); pdbr keeps its collection
  `PUT` semantics (unprovided columns keep their values on update), takes
  the key from the path (body key columns optional, `400 invalid_body` when
  they differ — PostgREST `PGRST115`), offers `PUT` only on paths whose
  key columns a `PUT` body may write (no identity/generated or
  `@omit create|update` keys), and answers `If-Match` with
  `400 if_match_unsupported`.
- **`POST` with `resolution=`**: always `201` (as PostgREST), but a
  single-object body skipped by `ignore-duplicates` answers `204` (pdbr
  returns objects, not arrays, for single-object bodies); `on_conflict=`
  without `resolution=` is `400 invalid_filter`.
- **`missing=default` is a no-op**: pdbr always inserts `DEFAULT` for keys a
  bulk element omits; PostgREST inserts `NULL` unless asked.
- **`Prefer: tx=` is honoured on writes only** (resource `POST`/`PUT`/
  `PATCH`/`DELETE` and volatile RPCs), and `server.tx_end: rollback*` rolls
  back writes only; reads never need a rollback. PostgREST applies it to
  every request.
- **`Prefer: timezone=` is matched case-insensitively** against
  `pg_timezone_names` and echoed with the canonical name; an unknown zone is
  ignored, or `400 invalid_preference` under `handling=strict`.
- **`columns=` lists missing keys as `DEFAULT`**, not `NULL`: a listed
  column an element omits takes the column default (`missing=default` is
  pdbr's only behaviour).
- **Casts** (`select=col::type`, aggregate casts) take allowlisted
  built-in scalar types and the API's enums only, never the `reg*` types,
  pseudo-types or composites; PostgREST casts to any type.
- **Spread embeds** take no alias of their own, cannot be ordered by in
  an aggregate select, and follow pdbr's duplicate-key rule (`400` on a key
  the parent already has); a to-many spread's arrays follow the embed's
  `order=` then its primary key, so their order is never unspecified.
  Spread grouping needs a unique target (below); plain (non-spread) embeds
  are still not grouping keys, and `count()` inside a to-one spread counts
  the related rows.
- **JSON paths on composite columns** work in `select=` only: composites are
  never filterable or orderable in pdbr, so `address->>city=eq.x` is
  `400 column_not_filterable`.
- **Duplicate response keys** are `400 invalid_select` unless the same
  column is selected again (`select=*,email`); PostgREST returns both keys.
- Error codes are pdbr's stable `code` vocabulary, not `PGRST…` numbers
  ([errors.md](errors.md)).

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
