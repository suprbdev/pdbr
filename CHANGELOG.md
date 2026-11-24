# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
as described in [docs/compatibility.md](docs/compatibility.md).

Entries up to and including the latest 0.x release below were seeded from the
GitHub release notes; the Unreleased section is maintained by hand and
becomes the release notes of the next tag.

## [Unreleased]

### Fixed

- **Volatile `SETOF` RPCs ran more than once or not at all.** `count=true`
  executed a volatile set-returning function twice (rows, then the total),
  and `limit=0` on a `SETOF <scalar>` or `SETOF <composite>` one compiled to
  `LIMIT 0`, so the function never ran while the `SETOF <table>` form did.
  A volatile set-returning function now runs exactly once per request
  whatever `count`, `limit`, `offset` or `limit=0` say: its output is
  materialised in a CTE that the page, the `has_next` look-ahead and the
  total read. `limit=0` is consistent across result kinds (function runs
  once, no rows, total still reported). Stable and immutable functions are
  unchanged.

## [0.20.0] - 2026-11-24

### Security

- **`limit=0` ran the whole selection (medium).** A root `limit=0` was
  costed at 1 but still projected its look-ahead row, running every embed,
  spread and computed column under it, so a deep select passed
  `limits.max_cost` and did the work of `limit=1`. It is now count-only:
  the selection is validated but not projected, and the look-ahead row
  that decides `has_next` reads only the root. Applies to `SETOF <table>`
  RPCs too.

### Changed

- `@behavior -filterBy` / `-orderBy` (PostGraphile V5) are honoured; `-filter`
  / `-order` still work with a deprecation warning. Unknown `@omit` actions
  and `@behavior` tokens are reported at build time, with pdbq's vocabulary
  shared through pdbcore ([upgrading](docs/upgrading.md#smart-tags-config-messages-and-embedding-api-pdbcore-api-hygiene)).
- Removed config keys name the reason they were removed; `PDBR_*` variables
  that double the underscore after a section are errors with a did-you-mean
  hint; `transactions.isolation` errors name the accepted values;
  `database.connect_timeout`, `database.max_conns`,
  `database.statement_timeout`, `auth.jwt.jwks_cache_ttl` (with a JWKS URL)
  and `watch.poll_interval` (with the watch on) are validated.
- `pdbr.LoadConfig` takes command-line values as `map[string]any` instead of
  a `*pflag.FlagSet`.
- Built against pdbcore's API hygiene changes (see its changelog).

### Fixed

- CSV output keeps carriage returns and line feeds inside values byte for
  byte (quoted, RFC 4180); a bare `\r` was dropped and `\n` became `\r\n`.
- A column whose name starts with `~` can no longer be named by any query
  parameter: casts, JSON paths and aggregates in `select=`, JSON-path and
  distance `order=` terms, `distinct=`, `on_conflict=` and `columns=` now
  answer `400 unknown_column` (`invalid_body` for `columns=`) like the plain
  forms. `select=*` still returns it and a JSON body still writes it.
- `401`/`403` answers from authentication carry `Vary: Accept` like every
  other API response.
- A live boot no longer alphabetises `schema.schemas`: the bare
  `/{table}` and `/rpc/{fn}` aliases come from the first configured schema,
  as on a schema-cache boot.

### Breaking

- `server.request_timeout` must be at least `1s`; `0` used to disable the
  timeout silently and is now a configuration error.

## [0.19.0] - 2026-11-24

### Added

- SECURITY.md, CONTRIBUTING.md, CHANGELOG.md and a versioning, deprecation and
  supported-versions policy ([docs/compatibility.md](docs/compatibility.md)).
- Release artifacts are accompanied by a CycloneDX SBOM, a keyless cosign
  signature of the checksums and GitHub build provenance attestations.
- Migration guide from PostgREST
  ([docs/migrating-from-postgrest.md](docs/migrating-from-postgrest.md)).
- `pdbr request --role NAME --claims '{json}'` runs a request as a role with
  claims; the default stays the connection role without claims.

### Changed

- `limit=0` (and `<rel>.limit=0`) is allowed, as in PostgREST: an empty
  page with its count headers; it was `400`.
- `go install github.com/suprbdev/pdbr/cmd/pdbr@vX.Y.Z` builds report the
  module version in `pdbr --version`.
- Contracts, nested mutations, partial mutations, rasters,
  cube/earthdistance, bit-vector distances, `ts_headline`, index-aware
  contrib ordering, polymorphic function resolution and the Go
  embedding/plugin API are marked experimental
  ([docs/compatibility.md](docs/compatibility.md)).

### Breaking

- The problem `code` vocabulary is closed and listed in
  [docs/errors.md](docs/errors.md#code-vocabulary). `invalid_filter` is split:
  reserved query parameters, the reference parameters and path keys answer
  `invalid_parameter`, and RPC and computed-column argument values answer
  `invalid_argument` (also in a `POST` body, which used `invalid_body`). A hint
  that selects no relation is `unknown_relation` instead of
  `ambiguous_relation`. The never-emitted `not_implemented` (501) and
  `compile.ErrNotImplemented` are removed. `embed_depth_exceeded` is
  `depth_exceeded`, as in pdbq.
- ETags are `W/"<row>.<repr>"`: the row as `select=*` renders it (hidden
  composite attributes no longer count) plus the response body, so `304` and
  the ETag follow embeds, computed columns and `Accept`; `If-Match` compares
  the row part, and is `400 if_match_unsupported` with `server.etags: false`
  instead of being ignored. API responses carry `Vary: Accept`.
- `/{schema}/rpc/{x}` always names a function: a table named `rpc` has no
  `/{pk}` path (list and `/by/` paths stay).

- The query grammar uses one naming rule each: the reference parameters and
  every pseudo-column live in a `~` namespace (`~near=`, `~vec=`,
  `select=~location_area`), so a column or function argument can no longer
  change what a request means; spatial operators are snake_case
  (`st_contains_properly`, `bbox_overlaps_or_left_of`, …) without aliases
  (`bbox_intersects2d` and `st_orderingequals` are gone); raster
  `intersects` is `st_intersects`; distances are `<metric>_distance` across
  order terms, scores and thresholds (`.dist` → `.st_distance`,
  `vec_cos` → `cosine_distance`, `trgm_dist` → `trgm_distance`,
  `earth_lt` → `earth_distance_lt`, …).

For the changes between 0.x and 1.0 see [docs/upgrading.md](docs/upgrading.md).

## [0.18.0] - 2026-11-23

### Breaking: identity and config (pre-1.0)

This release reshapes the configuration ahead of 1.0. See `docs/upgrading.md` ("0.x → 1.0") for the full before → after table.

- **`rls.*` is split into `auth`, `roles` and `claims`**: `auth: {mode: jwt|headers|none, jwt: {...}, headers: {prefix: X-Pdbr-Claim-, claims, role_claim, trusted_proxies}, mint_type}`, `roles: {switch, anonymous, default, allowed}`, `claims: {prefix, json_setting}`. Env vars move from `PDBR_RLS_*` to `PDBR_AUTH_*` / `PDBR_ROLES_*` / `PDBR_CLAIMS_*`.
- **Fail closed**: headers mode requires `auth.headers.trusted_proxies`; with `roles.anonymous` empty (the new default) requests without credentials get 401 with a `WWW-Authenticate` challenge; unknown auth modes are errors. `roles.switch: false` runs as the connection role but still verifies credentials.
- **Claims**: the per-claim prefix defaults to `jwt.claims` (PostGraphile-compatible; set `claims.prefix: pdbq.claims` to keep existing policies); `claims.json_setting` (e.g. `request.jwt.claims`) also sets all claims as one JSON value for PostgREST/Supabase-style policies.
- **Strict config**: unknown keys are rejected with a moved-to or did-you-mean hint; plugin names are snake_case (`smart_comments`, `simple_names`, `advanced_filters`, `nested_mutations`); plugin settings are typed and also read from env; `log.format` must be `text` or `json`.
- **Defaults**: `database.statement_timeout` 25s (below the 30s request timeout).

### Breaking: catalog format 10

- Identity `BY DEFAULT` columns are now writable (only `GENERATED ALWAYS` identity and generated columns are read-only).
- Overloaded functions are ordered deterministically (schema, name, argument types), so the kept overload is stable.
- PostgreSQL below 14 is refused at connect.
- Schema caches are rebuilt (format 10); cache files record their writer; a minor PostgreSQL upgrade no longer reports drift.

### Fixes

- CORS adds to `Vary` instead of replacing it and exposes the response headers browsers need; `Bearer` is case-insensitive; cardinality violations (SQLSTATE class 21) are 400 instead of 500; the CLI works on a default config.

### Changes
* feat(api)!: catalog format 10, writable identity BY DEFAULT columns (#91)
* feat(auth)!: split auth, roles and claims config; strict config (#92)

## [0.17.0] - 2026-11-23

### Security

- **Hidden rows inside composites and domains (medium).** The hidden-row-type check now follows domains, composite attributes and arrays: functions, columns and composites that contain a hidden table's row (or an unknown out-of-schema type) are not exposed.
- **Nested rows leaked hidden columns (medium).** Rows of visible tables nested in values (row arrays, composites, `RETURNS TABLE` columns, row-typed columns) now render as the table's `select=*` object — readable columns only, never raster bytes — in RPC results, computed columns, embeds, mutation responses, CSV and GeoJSON properties. JSON paths and casts on them read the rendered JSON. Row-holding columns are no longer filterable, orderable, distinct-able or groupable.
- **Integrity errors naming hidden objects.** Check constraints (`23514`), expression/partial unique indexes and exclusion constraints referencing hidden columns get the generic detail.
- **Headline CPU bound.** Documents are cut to `server.max_headline_document` characters (default 65536; longer json documents give a `null` headline) and priced by that length.
- PostGIS catalog views and other extension-owned relations are no longer exposed (pdbcore v0.16.0).

### Fixes

- **Index operator classes** (PostgreSQL 19 sweep): under `filters.indexed_only`, operators and orderings require an index with a matching operator class (pgvector metrics, trigram, inet on btree_gist).
- **`NOT NULL … NOT VALID` (PostgreSQL 18+)**: such columns are nullable in OpenAPI; NOT VALID foreign-key embeds are documented as possibly missing.

### Breaking changes

- Nested rows render through visible columns; row-holding columns lose filter/order/group surface.
- Some contrib operators/orderings disappear under `indexed_only` when operator classes don't match.
- Catalog cache format 9: existing schema caches are rebuilt.

### Changes
* feat(contrib): match index operator classes; NOT VALID foreign key embeds (#87)
* fix(security): hidden row types inside composites and domains; project nested rows (#88)

## [0.16.0] - 2026-11-23

### Highlights

- **GeoJSON precision and buffer** (pdbcore v0.14.0): `select=geom(precision=6,buffer=100)` on spatial columns and their pseudo-columns, also applied to `application/geo+json` responses.
- **jsonb full-text search** (PostgREST parity): `fts` / `plfts` / `phfts` / `wfts` and `rank` / `rank_cd` order terms on json/jsonb columns.
- **Headlines**: `<col>_headline` pseudo-columns (`ts_headline`) for columns with a full-text filter in the same scope; tsvector columns take `document=<col>`; options validated and bound, evaluated only on returned rows.
- **cube** (pdbcore v0.15.0): JSON points/boxes; `cs` / `cd` / `ov`; `vec_l2` / `vec_l1` / `vec_chebyshev` ordering, `_distance` scores and `vec_*_lt` thresholds against `vec=`.
- **earthdistance**: earth columns as `{lat, lon}`; `earth_lt.<metres>`, `order=<col>.earth_dist`, `<col>_earth_distance` against `latlon=<lat>,<lon>`; point columns get `earth_miles` / `earth_miles_lt` where unindexed surface is allowed.
- **Raster columns**: never returned (not in `select=*`, embeds, responses, CSV, GeoJSON or ETags; explicit select is 400), still writable as hex WKB. Pseudo-columns `<col>_width`, `_height`, `_num_bands`, `_srid`, `_pixel_width`, `_pixel_height`, `_envelope(precision=)`, `_value(band=)` (sampled at `near=`), `_summary_stats(band=)`; `intersects` filter.

### Breaking changes

- Raster columns are no longer returned by `select=*` (they previously came back as raw bytes).
- `latlon` is a soft-reserved query parameter on resources with earth columns.

### Changes
* feat(postgis): GeoJSON precision; feat(contrib): jsonb full-text search and headlines (#81)
* feat(contrib): cube and earthdistance; feat(postgis): raster columns (#82)

## [0.15.0] - 2026-11-23

### Security

- **Hidden rows leaked through functions (High).** Functions and computed columns returning rows of an `@omit` table, a contract-hidden table or an unexposed schema returned the whole row (hidden columns included), and OpenAPI named those tables. Functions whose result, arguments or output columns use a hidden table's row type (or an array of it), including polymorphic functions resolved to one, are no longer exposed; functions over types from schemas outside `schema.schemas` are skipped unless the type is known (the configured `rls.auth.jwt_type` is exempt).
- **Forgeable nested-write guard errors.** A user value could make a database error look like a nested-mutation guard failure and choose the 404 detail. Guard failures now carry a per-process random token.
- **Hidden names in integrity errors.** With `errors.detail: prod`, 23xxx errors that name a hidden table, column, foreign key or unique key get a generic per-SQLSTATE detail. The "missing required column(s)" body check no longer names contract-hidden columns.
- **Polymorphic functions and hidden types** (pdbcore v0.13.1): `@anyelement` / `@anycompatible` never resolve to hidden or non-introspected types.

### Highlights

- **hstore / ltree / fuzzystrmatch / intarray** (pdbcore v0.13.0): hstore values via `attrs->key` / `attrs->>key` in select, filters and `order=`, `hk_def`; ltree `<col>_nlevel`, `_parent`, `_lca(path=…)`, `_subpath(offset=…,length=…)`; `levenshtein=` with `levenshtein_lt.<n>`, `order=<col>.levenshtein` and `<col>_levenshtein_distance`; `soundex`, `metaphone`, `dmetaphone`; intarray `query_int.<q>` and `<col>_icount`.

### Breaking changes

- Functions over hidden row types or unknown out-of-schema types are no longer exposed.
- Integrity error details for hidden objects are generic in `prod` error mode.

### Changes
* feat(contrib): hstore and ltree accessors, fuzzystrmatch, intarray (#75)
* fix(security): function row leaks, forgeable guard errors, hidden names in integrity errors (#79)
* fix(compile): expect duplicate-key user error in derived fuzz case (#80)

## [0.14.0] - 2026-11-23

### Highlights

- **`Accept: application/geo+json`** (PostgREST parity): lists and `SETOF <table>` RPCs answer a GeoJSON FeatureCollection, key lookups and single-row RPCs a Feature. The geometry is the first spatial select item, or `?geometry=<key>`; other selected columns and embeds become properties. Filters, ordering, cursors, count, `Range`/206 work as for JSON; `406 not_acceptable` without a spatial column, with aggregates or for mutations. OpenAPI advertises the media type.
- **Selectable scores** (explicitly selected pseudo-columns, never in `*`): `<col>_distance` from `near=` (`(spheroid=true)` for metres on geometry), `<col>_similarity` / `_word_similarity` / `_strict_word_similarity` against `trgm=`, `<col>_l2_distance` / `_negative_inner_product` / `_cosine_distance` / `_l1_distance` from `vec=`, `<col>_hamming_distance` / `_jaccard_distance` on bit columns. Selecting a score without its reference parameter is 400.
- **pgvector distance thresholds**: `<col>=vec_l2_lt|vec_ip_lt|vec_cos_lt|vec_l1_lt.<distance>` (and `vec_hamming_lt` / `vec_jaccard_lt`), with `not.` and `or=()`; bit ordering `order=<col>.vec_hamming|vec_jaccard`.

### Breaking changes

- `geometry` is a soft-reserved query parameter on resources with a spatial column (yields to a column of that name).

### Changes
* feat(postgis): application/geo+json output (#73)
* feat(contrib): selectable distance and similarity scores (#74)

## [0.13.0] - 2026-11-23

### Highlights

- **pgvector**: vectors as JSON arrays in and out (PostgREST returns a string); nearest-neighbour ordering `order=<col>.vec_l2|vec_ip|vec_cos|vec_l1` with `vec=<reference>`. An HNSW index scan skips NULL vectors — add `<col>=not.is.null`.
- **unaccent** operators `unaccent_eq`, `unaccent_ilike`, `unaccent_sw`, `unaccent_includes`.
- **PostGIS**: `bbox_intersects2d`; geography perimeter in metres and spheroidal centroids. OpenAPI documents all of the above.

### Breaking changes

- Geography perimeter was in degrees and centroids planar; values change.

### Changes
* feat(contrib): pgvector ordering, unaccent operators, bbox_intersects2d (#72)

## [0.12.0] - 2026-11-23

### Highlights

- **Opt-in partial bulk writes**: with `server.partial_bulk_writes: true`, a bulk `POST`/`PUT` sent with `Prefer: partial=items` runs each element in its own SAVEPOINT and answers a per-element report (`207` when any element failed). At most `server.partial_bulk_max_items` (1000) elements; more is `413 body_too_large`. Default stays all-or-nothing.
- **Functions with table-row and polymorphic arguments**: row / row-array / composite-array arguments as JSON objects (POST body, or JSON text in GET query parameters), bound via `jsonb_populate_record`; OpenAPI `<resource>.row` components. Volatile functions with a leading row argument become POST-only RPCs. Polymorphic functions are exposed when tagged `@anyelement <type>` / `@anycompatible <type>`; untagged ones are skipped with one log line.

### Breaking changes

- Untagged polymorphic functions are no longer exposed.

### Changes
* feat(mutations): opt-in savepoints for partial success (#70)
* feat(functions): composite and polymorphic arguments (#71)

## [0.11.0] - 2026-11-23

### Highlights

- **Nested writes parity with pdbq**: single-object POST and single-row PATCH bodies can `connect` existing children on reverse relations, and PATCH can `update` / `delete` / `disconnect` related rows in both directions (forward: object of operations; backward: lists with `where`/`data`). All compiled into the one statement; every `connect`/`update`/`delete` target must match a row (missing, another parent's or RLS-hidden → `404 not_found`, nothing written). Per-action availability follows privileges and `@omit` (shared by runtime and OpenAPI). New setting `plugins.settings.nested-mutations.max_operations` (default 50). See docs/mutations.md.

### Breaking changes

- Nested forward `connect` now reports a missing target as `404` with the nested path in the detail (was a bare 404); goldens include a guard CTE.
- In nested-mutation depth, `update` counts a level like `create`.

### Changes
* feat(nested-mutations): reverse connect and nested update, delete, disconnect (#66)

## [0.10.1] - 2026-11-23

### Highlights

- **Faster embeds and keyset pages**: the page is cut before embed laterals join onto it, and `LIMIT` is compiled as a literal integer (capped by `server.max_page_size`) so Postgres reuses a cached generic plan. In the pg-vs-pdbq benchmark, nested embed throughput roughly doubles (3.2k → 6.3k RPS) and cursor walks go from 18k to 28k RPS (p99 21.8 → 2.0 ms).
- Requested rollbacks (`Prefer: tx=rollback`, `Prefer: max-affected` violations) are no longer counted as database errors in metrics (pdbcore v0.8.1).

### Changes
* chore(deps): bump pdbcore to v0.8.1 (#63)
* perf(compile): page before embed laterals; literal LIMIT (#64)

## [0.10.0] - 2026-11-23

### Highlights

- **Schema contracts** (pdbcore v0.8.0): named API variants per audience (`contracts:` config). Each contract gets its own routes and OpenAPI document under its base path (default `<server.base_path>/<name>`), built from that contract's catalog view; membership via `@contract a, b` and `@behavior +contract:x` / `-contract:x` smart comments. Contracts only remove surface; hidden columns are omitted everywhere. Watch mode rebuilds every contract; `pdbr schema print --contract X`. See docs/contracts.md.
- **`Prefer: tx=commit|rollback`** (PostgREST parity), gated by `server.tx_end` (`commit` default, `commit-allow-override`, `rollback`, `rollback-allow-override`). Applies to writes and volatile RPCs; a rolled-back write still returns its normal response.
- **`Prefer: timezone=<IANA name>`** sets the transaction time zone (validated against `pg_timezone_names`).
- **`columns=`** on POST/PUT/PATCH restricts which body keys are used; listed keys missing from an element get `DEFAULT`.
- `Preference-Applied` echoes honoured preferences; `Prefer: handling=strict` rejects invalid ones with 400.

### Breaking changes

- With `contracts:` configured, only the contracts' base paths are served.
- `columns` is a reserved query parameter on write routes.

### Changes
* feat(contracts): serve schema contracts per audience (#59)
* feat(prefer): tx and timezone preferences, columns parameter (#62)

## [0.9.0] - 2026-11-23

### Highlights

- **`@omit read` also hides a column from filters and ordering** (and from distinct, aggregates, spreads, keys and cursors) — intentional divergence from PostgREST/PostGraphile semantics so hidden values can't be bisected out. A hidden primary-key column removes the row route (`/{table}/{pk}`), keyset cursors (`after=`/`before=` → 400 `keyset_unsupported`) and `Location` headers for that table.
- **Watch mode**: pdbcore v0.7.1 no longer misses DDL committed while the watcher starts; `App.WatchReady()` exposes readiness.

### Breaking changes

- Tables whose primary key includes an `@omit read` column lose their row routes and keyset pagination (offset paging only).

### Changes
* fix(smart-comments)!: omit read implies omit filter and order (#56)
* chore(deps): bump pdbcore to v0.7.1 (#57)

## [0.8.0] - 2026-11-23

### Security

- **Claim spoofing in headers auth mode (fixed, pdbcore v0.7.0).** Variant claim header spellings (`X-…-Claim-User-Id` vs `X-…-Claim-User_id`) could override claims set by a trusted gateway. Each claim now has exactly one header spelling; malformed, repeated or colliding claim headers get 401; optional allowlist `rls.auth.header_claims`. Gateways must strip every client-supplied header with the claim prefix (any letter case).
- **ETag leak of hidden columns (fixed).** ETags hashed the whole row, including `@omit`/`@omit read` columns, which allowed recovering hidden values offline. ETags now cover only exposed columns (every ETag changes once).
- **Cost-limit bypass (fixed).** Embeds without a limit were costed at 10 rows but fetched up to `server.max_page_size`; they are now costed at what they fetch (also spreads, aggregated embeds and set-returning computed columns). The default `server.max_cost` is raised from 10000 to 100000 to keep one level of limitless embeds working.
- **CSV formula injection (fixed).** Cells starting with `=`, `+`, `-`, `@`, tab or CR are prefixed with `'` (`server.csv_escape_formulas`, default true).
- **Select aliases** are bound as parameters (previously quoted into the SQL text, unsafe with `standard_conforming_strings=off`).
- **JWKS hardening** (pdbcore v0.7.0): shared background key fetches with backoff, stale keys during IdP outages, no JWKS URL in client errors.

### Breaking changes

- Headers-mode requests with malformed/repeated/colliding claim headers get 401.
- ETag values change once; CSV string cells may gain a leading `'`.
- `server.max_cost` default is now 100000 and limitless embeds cost more.

### Changes
* fix(security)!: harden ETags, the cost limit, CSV and select aliases (#54)

## [0.7.0] - 2026-11-23

### Highlights (PostgREST parity)

- **JSON paths** in `select=`, filters and `order=`: `data->a->>b`, `tags->0`, quoted keys; also in aggregates and grouping. Keys and indexes are always bound parameters.
- **Column casts** `col::type` / `col->k::type` (built-in scalars and exposed enums).
- **`match` / `imatch`** regex operators and **`any` / `all` modifiers** (`eq(any).{a,b}`, `like(all).{…}`).
- **Spread embeds** `...rel(cols)` — to-one hoisting and to-many arrays — and aggregate grouping through to-one spreads.
- **Range pagination**: `Range` / `Range-Unit` request headers, `206 Partial Content`, `416 Range Not Satisfiable`.
- **Computed columns with arguments**: `select=id,excerpt(max_chars=80)`.
- **`/readyz` pings the database** (`server.readyz_timeout`); **`Retry-After`** on 503 responses.

### Breaking changes

- Counted pages that don't cover the whole result return `206` instead of `200` (also for `limit`/`offset` with `count=true`); an offset past the total is `416`.
- Duplicate response keys in `select=` are `400` (e.g. `id,email,email:full_name`, `*,mood::text`); they used to replace values silently.
- `select=col::type` now casts (was `400`).
- Functions with required extra arguments are exposed as computed columns.
- `/readyz` is no longer a liveness alias of `/healthz`.
- Text columns list `match`/`imatch` in OpenAPI `x-pdbr-filter-ops`.

### Changes
* feat(server): /readyz pings the database (#41)
* feat(server): Retry-After on 503 (#42)
* feat(select): computed-column arguments (#40)
* ci: run PG 14 and 19 on PRs, full matrix and docker on main (#45)
* ci: install postgresql-client only when psql is missing (#47)
* feat: Range request header, 206 Partial Content and 416 (#49)
* feat(select): JSON path operators and column casts (#43)
* feat(filter): match/imatch operators and any/all modifiers (#44)
* feat(select): spread embeds and spread grouping (#46)

## [0.6.0] - 2026-11-23

### Highlights

- **Prometheus `/metrics`** (off by default: `metrics.enabled`, optional separate listener `metrics.addr`): HTTP requests and latency by route class (resource, rpc, openapi, …), DB statement duration/errors/retries, connection-pool stats, watch rebuilds, compile time, cost-limit rejections and aggregate requests. Bring your own registry with `pdbr.WithMetrics`. See `docs/metrics.md`.
- **Named domain schemas in OpenAPI** (`server.openapi_domains`, default on): each domain over a built-in type gets `components/schemas/<domain>` with its comment and constraints; `pattern` / `minimum` / `maximum` are emitted for simple CHECKs. Columns, bodies, path keys, RPC arguments and results reference it.
- **Mutation-response embeds see the mutated rows**: embeds back to the table a POST/PATCH/PUT/DELETE changed show it as it stands after the statement (created/updated rows included, deleted rows excluded), under RLS and column grants.
- Domain-typed RPC arguments (e.g. a domain over `int[]` or geometry) parse and bind correctly; array casts for domain columns fixed.

### Behaviour changes

- Nested-mutation embeds now also see the table's existing rows (previously only the newly inserted ones).
- Schema caches from earlier versions are rejected (catalog format 8); regenerate with `pdbr schema dump`.

### Changes
* docs: drop version tiers from the feature checklist (#35)
* Let mutation-response embeds see the mutated rows (#34)
* feat(metrics): expose Prometheus /metrics (#36)
* feat(openapi): named domain schemas (#37)

## [0.5.0] - 2026-11-23

### Highlights (performance)

- **Responses built with `json_*` instead of `jsonb_*`** — 22–44% more req/s on embeds and large lists; GeoJSON output ~35% less DB time (pdbcore v0.5.0 json helpers).
- **Page rows pass straight through** to the response without decoding in Go — `Unwrap` for 1000 rows went from 386 µs to 2.8 µs; +64–80% req/s on large pages, +15% on small ones.
- **Keyset pagination on mixed-direction orders** is bounded by the leading column's index — a deep `after=` page went from 16 ms to 0.08 ms.
- pdbcore v0.5.0: indexed catalog lookups (faster introspection on large schemas).

### Fixes

- Range type constructor functions (e.g. `floatrange`, `floatmultirange`) are no longer exposed as RPCs.
- `App.Close` no longer hangs while watch mode is running.

### Behaviour changes

- Response object keys follow `select=` order (as in PostgREST) instead of jsonb's sorted order; `json` columns are returned as stored; large `float8` values use PostgreSQL's float output (e.g. `1e+20`). Bodies are ~1% larger before compression. ETags are unchanged.

### Changes
* Stop the watcher before closing the pool in App.Close (#29)
* Bound mixed-direction keyset scans by the leading order column (#27)
* Build response JSON with json_build_object and json_agg (#30)
* Pass page rows through to the response without decoding them (#31)
* Adopt pdbcore v0.5.0 (#32)

## [0.4.0] - 2026-11-23

### Security

**RLS bypass through role "none" (fixed, via pdbcore v0.4.0).** Earlier versions accepted the role name `none` from a role claim (JWT or headers mode) when `rls.allowed_roles` was empty or listed it. PostgreSQL treats `SET ROLE none` as `RESET ROLE`, so such a request ran as the database connection's login role — often the table owner or a superuser — and row-level security did not apply. Role names `none`, `public` and `pg_*` are now rejected at config load (`rls.anonymous_role`, `rls.default_role`, `rls.allowed_roles`) and per request (`403 role_not_allowed` before any SQL). Deployments should set `rls.allowed_roles` explicitly and should not connect as a superuser or table owner. Upgrading is recommended for all users of v0.3.0 and earlier.

### Highlights

- **Aggregates** (PostgREST v12 parity, opt-in via `server.aggregates`): `select=amount.sum(),category`, `count()`, `avg`/`max`/`min`, aliases, allowlisted casts, aggregates in to-many embeds, spatial `extent()`/`union()`/`collect()`/`centroid()`. See `docs/aggregates.md`.
- **One round trip per request under RLS** (`pgexec.QueryTx`): identity, claims and statement are pipelined — ~+69% req/s, p50 670 µs → 388 µs in a 16-client RLS benchmark.
- **OpenAPI**: rendered once per schema, served compact and pre-gzipped with `ETag`/304, repeated parameters hoisted to `components/parameters` (a 1000-table document went from 84 MB to 34 MB / 0.55 MB gzipped; peak memory under concurrent fetches from 3.4 GB to 0.4 GB).
- Custom range types get range filters, structured bodies and OpenAPI schemas; watch mode ignores unrelated DDL and skips rebuilds when the catalog is unchanged.
- PostGIS operand SQL is a single value-independent expression (stable prepared statements).

### Breaking changes

- `transactions.isolation: read_committed` (and unset) now runs at the server's `default_transaction_isolation` instead of an explicit `READ COMMITTED` (identical unless that default was changed).
- The OpenAPI document references shared parameters via `$ref` (`components/parameters`) and is served compact.
- Embeds can't be used as grouping keys in aggregate selects (pdbr has no spread embeds).
- Schema caches from earlier versions are rejected (catalog format 7); regenerate with `pdbr schema dump`.

### Changes
* Serve a pre-rendered, pre-gzipped OpenAPI document and hoist repeated parameters (#23)
* Aggregate functions in select= (PostgREST v12 parity, spatial extensions) (#22)
* docs: drop stale status banner and PLAN.md references (#24)
* Adopt pdbcore v0.4.0 (security: reject role none) (#25)

## [0.3.0] - 2026-11-23

### Highlights

- **PostgREST parity**: bulk `PATCH`/`DELETE` by filter (unfiltered writes refused unless `server.allow_unfiltered_mutations`), `Prefer: handling=strict` + `max-affected`, `Prefer: count=exact|planned|estimated` with `Content-Range`, `Prefer: resolution=merge-duplicates|ignore-duplicates`, `PUT` on single-row paths. Divergences are listed in `docs/features.md` ("Differences from PostgREST").
- **Contrib extensions**: PostgREST full-text operators `fts`/`plfts`/`phfts`/`wfts` (with `(config)`), range operators `ov`/`cs`/`cd`/`sl`/`sr`/`nxr`/`nxl`/`adj`, `isdistinct`; plus trigram (`trgm_sim`, `trgm_wsim`, `trgm_swsim`, distance ordering with `trgm=`), hstore, ltree and inet operators; full-text rank ordering; case-insensitive citext everywhere (keys, joins, keyset paging), even when the extension schema is off `search_path`. See `docs/contrib.md`.
- **PostgreSQL 18 temporal keys**: PERIOD foreign keys embed by range overlap (to-many), `WITHOUT OVERLAPS` keys get no single-row routes, NOT ENFORCED foreign keys embed as `null` when dangling.
- **RPC**: omitted arguments use their `DEFAULT`, VARIADIC arguments, `RETURNS TABLE`/`OUT`-parameter functions are routed (with `select=`, filters, `order=`).
- **PostGIS**: operands take the column's SRID; PostGIS input errors are 400s.
- Partitions are hidden by default (`schema.expose_partitions`); foreign keys on partitioned tables no longer produce `ambiguous_relation`.
- CI covers PostgreSQL 14–19.

### Breaking changes

- Omitting an RPC argument without a default is `400 missing_argument` (it used to be sent as `NULL`).
- Partition tables are not resources unless `schema.expose_partitions: true`.
- `pdbr.SchemaConfig` is pdbr's own type embedding `confx.Schema` (`SchemaConfig{Schema: confx.Schema{...}}` for literals).
- Single-object `POST` on tables without a single-column key returns `201` (was `200`).
- Collection paths now allow `PATCH`/`DELETE`; `Allow` headers change accordingly.
- A stray `near=` or `trgm=` without a matching distance order term is `400 invalid_filter` (was silently ignored).
- `trgm` (like `near`) is a reserved query parameter unless a column or argument has that name.
- hstore columns are `type: object` in OpenAPI and accept JSON objects.
- Schema caches from earlier versions are rejected; regenerate with `pdbr schema dump`.

### Changes
* Adopt pdbcore v0.3.0: SRID-aware spatial values, named-notation RPC, RETURNS TABLE, partitions (#14)
* Bulk PATCH/DELETE by filter, Prefer handling and max-affected (#11)
* Prefer count= and Content-Range (#12)
* Prefer resolution= on POST and PUT on single-row paths (#13)
* Support PostgreSQL 18 temporal keys, PERIOD and NOT ENFORCED foreign keys (#16)
* Add full-text, trigram, hstore, ltree, range and inet filters (#15)

## [0.2.0] - 2026-11-22

### Changes
* ci: test e2e against PostgreSQL 14-19 (#6)
* Expose the pdbcore v0.2.0 PostGIS operators and accessors (#8)

## [0.1.0] - 2026-11-22

### Changes
* Depend on tagged pdbcore v0.1.0 (#1)

### New Contributors
* @archlemon made their first contribution in https://github.com/suprbdev/pdbr/pull/1

[Unreleased]: https://github.com/suprbdev/pdbr/compare/v0.18.0...HEAD
[0.18.0]: https://github.com/suprbdev/pdbr/compare/v0.17.0...v0.18.0
[0.17.0]: https://github.com/suprbdev/pdbr/compare/v0.16.0...v0.17.0
[0.16.0]: https://github.com/suprbdev/pdbr/compare/v0.15.0...v0.16.0
[0.15.0]: https://github.com/suprbdev/pdbr/compare/v0.14.0...v0.15.0
[0.14.0]: https://github.com/suprbdev/pdbr/compare/v0.13.0...v0.14.0
[0.13.0]: https://github.com/suprbdev/pdbr/compare/v0.12.0...v0.13.0
[0.12.0]: https://github.com/suprbdev/pdbr/compare/v0.11.0...v0.12.0
[0.11.0]: https://github.com/suprbdev/pdbr/compare/v0.10.1...v0.11.0
[0.10.1]: https://github.com/suprbdev/pdbr/compare/v0.10.0...v0.10.1
[0.10.0]: https://github.com/suprbdev/pdbr/compare/v0.9.0...v0.10.0
[0.9.0]: https://github.com/suprbdev/pdbr/compare/v0.8.0...v0.9.0
[0.8.0]: https://github.com/suprbdev/pdbr/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/suprbdev/pdbr/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/suprbdev/pdbr/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/suprbdev/pdbr/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/suprbdev/pdbr/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/suprbdev/pdbr/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/suprbdev/pdbr/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/suprbdev/pdbr/releases/tag/v0.1.0
