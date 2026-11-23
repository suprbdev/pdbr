# Configuration

pdbr layers configuration from four sources, listed here from lowest
precedence to highest:

1. **Built-in defaults** — the values in the table below.
2. **YAML file** — `--config path.yaml`, defaulting to `./pdbr.yaml` (or
   `./pdbr.yml`) when present.
3. **Environment variables** — `PDBR_` prefix, `_` separates levels, `__`
   preserves an underscore inside a key.
4. **Command-line flags** — named after config paths: `--database.url`,
   `--roles.switch=false`. Only flags that were actually set on the command
   line are applied, so a flag's default never overrides the file or env.

The **complete annotated reference** lives at
[`examples/pdbr.example.yaml`](../examples/pdbr.example.yaml). It is
generated from the config structs' `koanf` and `doc` tags
(`pdbr config example`), so it cannot drift from the implementation — never
hand-edit it; regenerate with `make example-config`.

## Strict loading

Configuration is checked as it loads, before validation:

- A key in the YAML file that pdbr does not know is an error. The message
  names the key it moved to when it was renamed (`unknown key rls.enabled:
  moved to roles.switch`, `unknown key server.tx_end: moved to
  transactions.end`; see [upgrading.md](upgrading.md)) or the closest
  valid key (`unknown key server.adr: did you mean server.addr?`).
- A `PDBR_*` environment variable whose first segment names a config
  section (`PDBR_SERVER_…`, `PDBR_AUTH_…`, `PDBR_RLS_…` for the removed
  section, or a near miss of one) must map to a known key, with the same
  hints (`unknown environment variable PDBR_RLS_AUTH_JWT__SECRET
  (rls.auth.jwt_secret): moved to PDBR_AUTH_JWT_SECRET`). Other variables
  with the prefix, such as `PDBR_TEST_DATABASE_URL`, are ignored.
- Values are decoded into the typed fields, so environment strings set
  booleans, numbers, durations and lists, plugin settings included
  (`PDBR_PLUGINS_SETTINGS_ADVANCED__FILTERS_RELATIONS=false`); a value that
  does not fit its field is an error.

## Environment variable mapping

Strip the `PDBR_` prefix, lowercase, then: `__` becomes a literal `_`, every
remaining `_` becomes a `.` level separator.

| Env var | Key |
|---|---|
| `PDBR_DATABASE_URL` | `database.url` |
| `PDBR_LIMITS_MAX__PAGE__SIZE=50` | `limits.max_page_size` |
| `PDBR_SERVER_BASE__PATH=/v1` | `server.base_path` |
| `PDBR_TRANSACTIONS_PARTIAL_MAX__ITEMS=200` | `transactions.partial.max_items` |
| `PDBR_AUTH_JWT_SECRET=...` | `auth.jwt.secret` |
| `PDBR_ROLES_ANONYMOUS=anonymous` | `roles.anonymous` |
| `PDBR_PLUGINS_SETTINGS_NESTED__MUTATIONS_MAX__DEPTH=5` | `plugins.settings.nested_mutations.max_depth` |
| `PDBR_FILTERS_INDEXED__ONLY=false` | `filters.indexed_only` |
| `PDBR_SCHEMA_SCHEMAS=public,audit` | `schema.schemas` (lists are comma-separated) |

## Commands

```console
$ pdbr config init      # write a minimal starter pdbr.yaml
$ pdbr config validate  # check the effective config, exit non-zero on error
$ pdbr config example   # print the full annotated reference
```

See [cli.md](cli.md) for details.

## The knobs

| Key | Type | Default | Notes |
|---|---|---|---|
| `database.url` | string | — | required; standard `PG*` env vars are honoured when empty |
| `database.max_conns` | int | 10 | maximum pooled connections; each request uses at most one |
| `database.connect_timeout` | duration | `10s` | timeout for establishing a connection |
| `database.statement_timeout` | duration | `25s` | per-statement timeout on every request (`0` disables); expiry → `504 pg:57014`. Keep it below `server.request_timeout` (`30s`) so a slow statement fails with a database timeout the client can act on before the request itself is cut off |
| `server.addr` | string | `:8080` | listen address |
| `server.base_path` | string | `""` | prefix for every API route, e.g. `/api`; `""` (the default, as in PostgREST) mounts the API at the root. `/healthz` and `/readyz` are never prefixed |
| `server.request_timeout` | duration | `30s` | overall HTTP request timeout; expiry → `504` problem |
| `server.readyz_timeout` | duration | `2s` | deadline of the pool ping behind `GET /readyz`; must be `> 0` |
| `server.cors_origins` | []string | `[]` | allowed CORS origins (exact match, or `*`); empty disables CORS headers and preflight |
| `server.compression` | bool | `false` | gzip responses when the client sends `Accept-Encoding: gzip` |
| `server.read_only` | bool | `false` | every write → `405` (`code: read_only`); runtime-toggleable via `App.SetReadOnly` |
| `server.etags` | bool | `true` | weak `ETag` on single-row GETs, `If-None-Match` → `304`, `If-Match` on `PATCH`/`DELETE` → `412`; the hash covers the exposed columns only (`@omit` / `@omit read` columns are left out) |
| `server.csv` | bool | `true` | `Accept: text/csv` on lists and set-returning RPCs |
| `server.csv_escape_formulas` | bool | `true` | prefix CSV header and string cells starting with `=`, `+`, `-`, `@`, tab or CR with `'` so spreadsheets do not run them as formulas (CSV injection); JSON numbers are never escaped; `false` writes cells verbatim. See [api.md](api.md#content-negotiation) |
| `openapi.enabled` | bool | `true` | serve `GET {base}/openapi.json`; it describes every exposed table, column, relation and function to unauthenticated callers, like pdbq's introspection (also on by default). Set `false` to hide the schema; see [openapi.md](openapi.md) |
| `openapi.title` | string | `pdbr` | the document's `info.title` |
| `openapi.server_url` | string | — | absolute origin clients reach the API at (e.g. `https://api.example.com` behind a proxy), put before the base path in `servers[0].url`; empty leaves it relative (the base path, `/` at the root). Must be an absolute URL without query or fragment |
| `limits.max_body_bytes` | int | 1048576 | maximum request body size (1 MiB); over → `413` |
| `limits.max_depth` | int | 5 | maximum `select=` embed nesting; over → `400 depth_exceeded` |
| `limits.max_cost` | int | 100000 | per-request cost limit (1 + Σ list multipliers, a list counting the rows its SQL can fetch: its `limit`, else `limits.max_page_size`, to-many embeds included); over → `400 cost_exceeded`; `@costMultiplier` tunes estimates (see [pagination.md](pagination.md#cost-limit), [smart-comments.md](smart-comments.md)) |
| `limits.max_page_size` | int | 100 | largest accepted `limit` (larger → `400 page_size_exceeded`); also the default when `limit` is absent; see [pagination.md](pagination.md) |
| `limits.max_headline_document` | int | 65536 | characters of a document a [full-text headline](contrib.md#headlines) reads: a text document is cut to that many characters, a longer json/jsonb document gives a `null` headline; each headline costs 10 per started 8192 of them in `limits.max_cost` |
| `schema.domains` | bool | `true` | each domain over a built-in type gets a named component schema (`components/schemas/<domain>`) referenced by columns, arguments and results; `false` inlines the base type's schema. See [openapi.md](openapi.md#domains) |
| `schema.schemas` | []string | `[public]` | schema allowlist; resources in the first entry also get bare `/{table}` aliases |
| `schema.cache_path` | string | — | boot from `pdbr schema dump` output; see [caching.md](caching.md) |
| `schema.functions` | bool | `true` | expose functions as `/rpc/{fn}` routes and computed columns ([rpc.md](rpc.md)) |
| `schema.expose_partitions` | bool | `false` | also expose each partition of a partitioned table as its own resource; off, only the partitioned parent is a resource (its rows already span every partition) — see [api.md](api.md#what-gets-generated) |
| `filters.indexed_only` | bool | `true` | only indexed/PK/unique columns filterable and orderable; see [filtering.md](filtering.md) |
| `filters.allow_columns` | map[string][]string | `{}` | per-table extra filterable columns keyed by `schema.table`, overriding `indexed_only`; also the only way to admit operators no index serves (unaccent, fuzzystrmatch, the earthdistance `<@>` surface on point columns) and the raster `st_intersects` filter, whose GiST index is on an expression |
| `mutations.allow_unfiltered` | bool | `false` | let `PATCH`/`DELETE {base}/{table}` run without a filter (every row the role can see); off → `400 unfiltered_mutation`. The guard checks that a filter (or `!inner` embed) is present, not its selectivity — a match-all filter passes; bound writes with `Prefer: handling=strict, max-affected=N`. See [mutations.md](mutations.md#bulk-patch--delete-by-filter) |
| `aggregates.enabled` | bool | `false` | aggregate functions in `select=` (`count()`, `col.sum()`, `col.avg()`, `col.max()`, `col.min()`; spatial `col.extent()`/`union()`/`collect()`/`centroid()`), grouped by the other selected items — PostgREST `db-aggregates-enabled`; off → `400 aggregates_disabled`. An aggregate reads every row its filters match (`limit` bounds groups, not the scan), so index what you expose and keep `database.statement_timeout`; see [aggregates.md](aggregates.md) |
| `auth.mode` | string | `jwt` | credential source: `jwt` (bearer JWT), `headers` (claims from a trusted gateway) or `none` (no credentials); see [rls.md](rls.md#claim-sources-authmode) |
| `auth.jwt.secret` | string | — | HMAC secret verifying HS256/384/512 tokens (ignored for verification when `jwks_url` is set) and signing minted tokens |
| `auth.jwt.secret_file` | string | — | file holding the HMAC secret (trailing newlines stripped), instead of `secret`; must be readable at startup |
| `auth.jwt.jwks_url` | string | — | JWKS endpoint for RS*/ES* verification; takes precedence over the secret. `jwt` mode needs a secret, secret file or JWKS URL |
| `auth.jwt.jwks_cache_ttl` | duration | `1h` | JWKS cache lifetime; unknown `kid` triggers an early refresh. Keys are refreshed in the background with backoff after a failed fetch; fetch errors are logged (endpoint and cause) and clients only see `401 invalid_token` |
| `auth.jwt.issuer` | string | — | expected `iss`; empty skips |
| `auth.jwt.audience` | string | — | expected `aud`; empty skips |
| `auth.jwt.leeway` | duration | `0s` | clock skew tolerated on `exp`, `nbf` and `iat`; must be `>= 0` |
| `auth.jwt.role_claim_path` | string | `role` | path of the role claim: dot-separated keys, array indexes, double-quoted keys (`app_metadata.role`, `realm_access.roles[0]`, `"https://example.com/role"`) |
| `auth.headers.prefix` | string | `X-Pdbr-Claim-` | header prefix mapped to claims in headers mode; one spelling per claim (`User-Id` → `user_id`, matched in any case); a prefixed header with an underscore or other punctuation, repeated, or folding onto another claim header → `401 invalid_token`. The gateway must strip every client-supplied header with the prefix, in any letter case ([rls.md](rls.md)) |
| `auth.headers.claims` | []string | `[]` | claim names (e.g. `user_id`) accepted from claim headers; the role claim is always accepted; any other claim header → `401 invalid_token`; empty accepts any claim. Names are lower-case letters and digits joined by single underscores |
| `auth.headers.role_claim` | string | `role` | claim carrying the role in headers mode (sent as `<prefix>Role`) |
| `auth.headers.trusted_proxies` | []string | `[]` | CIDRs/IPs allowed to send claim headers; **required** in headers mode; claim headers from other peers → `401 invalid_token` |
| `auth.mint_type` | string | — | schema-qualified composite (e.g. `public.jwt`); functions returning it yield a minted HS256 token; needs `auth.jwt.secret` or `secret_file` |
| `roles.switch` | bool | `true` | run each request as its resolved role (`SET LOCAL ROLE`); `false` runs every request as the connection role, still verifying credentials and setting claims (startup warning) |
| `roles.anonymous` | string | — | role of requests without credentials; empty answers them `401 auth_required` (`jwt`/`headers` mode); required in mode `none` while `switch` is on |
| `roles.default` | string | — | role of authenticated requests without a role claim; empty falls back to `roles.anonymous` |
| `roles.allowed` | []string | `[]` | roles a request may assume via its role claim; empty allows any (subject to grants, startup warning); rejected → `403 role_not_allowed` |
| `claims.prefix` | string | `jwt.claims` | namespace of the per-claim settings (`current_setting('jwt.claims.user_id', true)`); empty sets none |
| `claims.json_setting` | string | — | setting receiving every claim as one JSON object (`{}` without credentials), e.g. `request.jwt.claims` for PostgREST/Supabase policies; must not lie under `claims.prefix` |
| `transactions.isolation` | string | `read_committed` | or `repeatable_read`, `serializable`; `read_committed` runs in the pipeline's implicit transaction at the server's `default_transaction_isolation` (read committed unless changed) |
| `transactions.max_retries` | int | 0 | automatic re-runs on serialization failure/deadlock (40001/40P01); exhausted → `409` |
| `transactions.end` | string | `commit` | how a write's transaction ends (PostgREST `db-tx-end`): `commit`, `commit-allow-override`, `rollback`, `rollback-allow-override`. `rollback*` runs every write (resource writes and volatile RPCs), answers with its result and discards its effects; the `-allow-override` forms honour `Prefer: tx=commit\|rollback` (echoed in `Preference-Applied`), the others ignore it (`400 invalid_preference` under `handling=strict`). See [mutations.md](mutations.md#prefer) |
| `transactions.partial.enabled` | bool | `false` | let a bulk `POST` or `PUT` opt in with `Prefer: partial=items` to running each element in its own savepoint: a failing element is reported without rolling back the others, answered as a per-element report (`207` when one failed). Not honoured for `PATCH`, `DELETE` or a single object. See [mutations.md](mutations.md#partial-success-for-bulk-writes) |
| `transactions.partial.max_items` | int | `1000` | most elements a bulk body may carry with `Prefer: partial=items` (each element is its own statement and savepoint); more answers `413 body_too_large` |
| `watch.enabled` | bool | `false` | dev only; refuses to combine with cache |
| `watch.poll_interval` | duration | `5s` | fallback polling when event triggers can't be installed |
| `watch.channel` | string | `pdbr_ddl` | NOTIFY channel used by the DDL event trigger |
| `errors.detail` | string | `prod` | `dev` adds full PG detail + `pg` object to problems; `strict` hides even constraint-violation messages |
| `plugins.disabled` | []string | `[]` | built-in plugins to disable: `smart_comments`, `advanced_filters`, `postgis`, `nested_mutations` (other names are rejected); see [plugins.md](plugins.md) |
| `plugins.settings.advanced_filters.relations` | bool | `true` | relation filters (`rel.col=op.v`, `rel!none.`, `rel!every.`, `rel=exists.`) |
| `plugins.settings.advanced_filters.computed` | bool | `true` | filtering and ordering on computed columns (follows `schema.functions`) |
| `plugins.settings.advanced_filters.relation_order` | bool | `true` | ordering by a related column (`order=rel(col).asc`); off when `relations` is |
| `plugins.settings.advanced_filters.relations_opt_in` | bool | `false` | only FKs tagged `@filterable`/`@sortable` get relation filters and ordering |
| `plugins.settings.advanced_filters.computed_opt_in` | bool | `false` | only computed functions tagged `@filterable`/`@sortable` get filters and ordering |
| `plugins.settings.postgis.filters` | bool | `true` | spatial filter operators (`st_*`, `bbox_*`), raster `st_intersects` |
| `plugins.settings.postgis.derived` | bool | `true` | derived pseudo-columns (`~<col>_area`, …, raster `~<col>_width`, …) |
| `plugins.settings.postgis.ordering` | bool | `true` | `order=<col>.st_distance` and `~<col>_st_distance` with `~near=` |
| `plugins.settings.postgis.projection` | bool | `true` | `select=col(transform=…,simplify=…,buffer=…,precision=…)` |
| `plugins.settings.postgis.aggregates` | bool | `true` | spatial aggregates (`col.extent()`, `union()`, `collect()`, `centroid()`; also needs `aggregates.enabled`) |
| `plugins.settings.nested_mutations.max_depth` | int | `3` | deepest create/update nesting (root = 1); over → `400 depth_exceeded`; must be `>= 1` |
| `plugins.settings.nested_mutations.max_operations` | int | `50` | most nested operations per request; over → `400 invalid_body`; must be `>= 1` |
| `log.level` | string | `info` | `debug`, `info`, `warn`, `error` |
| `log.format` | string | `text` | `text` or `json` (other values are rejected) |
| `metrics.enabled` | bool | `false` | serve Prometheus metrics and record them; see [metrics.md](metrics.md) |
| `metrics.path` | string | `/metrics` | URL path of the metrics endpoint (outside `base_path`) |
| `metrics.addr` | string | — | separate listen address for the metrics endpoint (e.g. `:9090`); empty serves it on `server.addr` |
| `contracts` | list | `[]` | schema contracts: `{name, default: include\|exclude, path}` entries (or bare names), each a subset of the API under its own base path (default `<base_path>/<name>`) with its own OpenAPI document; when set only the contracts are served; see [contracts.md](contracts.md) |

## Validation rules

`pdbr config validate` (and every command that loads config) rejects the
effective configuration when any of these fail; all violations are reported
together.

1. `errors.detail` is one of `dev`, `prod`, `strict`.
2. `auth.mode` is one of `jwt`, `headers`, `none`.
3. Every `auth.headers.trusted_proxies` entry is a valid IP or CIDR block.
4. In `jwt` mode, `auth.jwt.secret`, `auth.jwt.secret_file` or
   `auth.jwt.jwks_url` is set; `secret` and `secret_file` are not both set;
   a `secret_file` is readable and not empty; `jwks_url` is an http(s) URL;
   `auth.jwt.role_claim_path` parses; `auth.jwt.leeway >= 0`.
5. `auth.mint_type` requires `auth.jwt.secret` or `auth.jwt.secret_file`
   (minting signs with the HMAC secret; JWKS keys are verify-only).
6. `auth.mint_type`, when set, is schema-qualified (`public.jwt`).
7. In `headers` mode, `auth.headers.trusted_proxies` lists at least one
   entry, `auth.headers.prefix` is letters, digits and hyphens ending in a
   hyphen, and `auth.headers.role_claim` and every `auth.headers.claims`
   entry are lower-case letters and digits joined by single underscores.
8. `transactions.isolation` is one of `read_committed`, `repeatable_read`,
   `serializable`.
9. `watch.enabled` and `schema.cache_path` are not both set.
10. `watch.channel` matches `[A-Za-z0-9_]+` when `watch.enabled`.
11. `schema.schemas` lists at least one schema.
12. `transactions.max_retries >= 0`.
13. `limits.max_page_size >= 1`.
14. `log.level` is one of `debug`, `info`, `warn`, `error`; `log.format` is
    `text` or `json`.
15. `server.base_path` is empty or starts with `/` and does not end with `/`.
16. `limits.max_depth >= 1`.
17. `roles.anonymous`, `roles.default` and every `roles.allowed` entry,
    when set, match `[A-Za-z0-9_]+` and are not `none`, `public` or a
    `pg_*` role, in any letter case (`set_config('role', 'none', true)` is
    `RESET ROLE`, which would run the request as the connection role); in
    `auth.mode: none` with `roles.switch` on, `roles.anonymous` is set.
18. `claims.prefix` is empty or dot-separated identifiers whose first
    component is not a server or extension settings namespace (`pg_*`,
    `plpgsql`, `auto_explain`, `postgis`, …); `claims.json_setting`, when
    set, is a valid custom setting name not under `claims.prefix`.
19. `metrics.path` starts with `/`, is not `/` itself and has no query or
    fragment.
20. `metrics.addr`, when set, is a `host:port` listen address and differs
    from `server.addr`.
21. With `metrics.enabled` and no `metrics.addr`, `metrics.path` is not
    `/healthz`, `/readyz`, `server.base_path` or a path below it (with an
    empty `base_path`: not `/openapi.json`); see
    [metrics.md](metrics.md#on-the-main-listener-metricsaddr-empty).
    With `contracts`, the same holds for every contract's base path instead
    of `server.base_path`.
22. `limits.max_headline_document >= 1`.
23. `contracts` entries have unique names of lower-case letters, digits,
    `_` and `-` starting with a letter, a `default` of `include` or
    `exclude`, and unique, non-overlapping clean paths; they require the
    `smart_comments` plugin (see [contracts.md](contracts.md#configuration)).
24. Every `plugins.disabled` entry names a built-in plugin (a near miss such
    as `nested-mutations` suggests `nested_mutations`);
    `plugins.settings.nested_mutations.max_depth` and `max_operations` are
    `>= 1`.
25. `limits.max_body_bytes >= 1` and `limits.max_cost >= 1`.
26. `transactions.end` is one of `commit`, `commit-allow-override`,
    `rollback`, `rollback-allow-override`; `transactions.partial.max_items
    >= 1`.
27. `openapi.server_url`, when set, is an absolute URL (scheme and host)
    without query or fragment.

The default configuration is therefore not valid on its own: it verifies
JWTs and needs `auth.jwt.secret`, `secret_file` or `jwks_url` (or
`auth.mode: none`). `pdbr config init` writes a starter that runs with only
`database.url` set.

## Differences from pdbq

pdbq and pdbr share the shared sections (`database`, `filters`, `auth`,
`roles`, `claims`, `watch`, `errors`, `plugins`, `log`, `metrics`,
`contracts`) and use the same names for the concepts both have:
`limits.{max_body_bytes,max_depth,max_cost,max_page_size,max_headline_document}`,
`transactions.{isolation,max_retries,partial.enabled,partial.max_items}`,
`schema.domains` and `aggregates.enabled`. Both serve their schema
document by default (pdbr `openapi.enabled`, pdbq `server.introspection`).

| pdbr only | pdbq only |
|---|---|
| `server.base_path`, `server.etags`, `server.csv`, `server.csv_escape_formulas` | `server.graphiql`, `server.introspection`, `server.document_cache.*` |
| `openapi.*` (the REST schema document) | `persisted_queries.*`, `subscriptions.*`, `live.*`, `incremental.*` |
| `mutations.allow_unfiltered` | `limits.max_query_tokens`, `max_input_depth`, `max_selections`, `max_introspection_bytes`, `max_field_comparisons` (GraphQL document limits) |
| `transactions.end` (PostgREST `db-tx-end`) | `transactions.per_request` (a REST request is one operation, so it has no analogue) |

`limits.max_depth` bounds `select=` embed nesting in pdbr (default 5) and
selection depth in pdbq (default 15); `limits.max_cost` uses each product's
own estimate and scale.

## Flags

Flags mirror koanf paths, so precedence needs no mapping table. Every
subcommand that loads configuration accepts them plus `--config`.

| Flag | Type | Notes |
|---|---|---|
| `--config` | string | path to config file (default: `./pdbr.yaml` / `./pdbr.yml` if present) |
| `--database.url` | string | |
| `--server.addr` | string | |
| `--server.base_path` | string | |
| `--server.cors_origins` | []string | comma-separated or repeated |
| `--server.compression` | bool | |
| `--server.read_only` | bool | |
| `--openapi.enabled` | bool | |
| `--openapi.title` | string | |
| `--openapi.server_url` | string | |
| `--limits.max_headline_document` | int | |
| `--schema.schemas` | []string | comma-separated or repeated |
| `--schema.cache_path` | string | |
| `--schema.expose_partitions` | bool | |
| `--schema.domains` | bool | |
| `--mutations.allow_unfiltered` | bool | |
| `--aggregates.enabled` | bool | |
| `--transactions.end` | string | `commit`, `commit-allow-override`, `rollback`, `rollback-allow-override` (shell-completed) |
| `--transactions.partial.enabled` | bool | |
| `--transactions.partial.max_items` | int | |
| `--auth.mode` | string | `jwt`, `headers`, `none` (shell-completed) |
| `--roles.switch` | bool | |
| `--roles.anonymous` | string | |
| `--watch.enabled` | bool | |
| `--filters.indexed_only` | bool | |
| `--errors.detail` | string | `dev`, `prod`, `strict` (shell-completed) |
| `--log.level` | string | `debug`, `info`, `warn`, `error` (shell-completed) |
| `--metrics.enabled` | bool | |
| `--metrics.path` | string | |
| `--metrics.addr` | string | |

Keys without a flag are still settable through YAML or `PDBR_*` env vars.
