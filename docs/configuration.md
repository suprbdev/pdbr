# Configuration

pdbr layers configuration from four sources, listed here from lowest
precedence to highest:

1. **Built-in defaults** — the values in the table below.
2. **YAML file** — `--config path.yaml`, defaulting to `./pdbr.yaml` (or
   `./pdbr.yml`) when present.
3. **Environment variables** — `PDBR_` prefix, `_` separates levels, `__`
   preserves an underscore inside a key.
4. **Command-line flags** — named after config paths: `--database.url`,
   `--rls.enabled=false`. Only flags that were actually set on the command
   line are applied, so a flag's default never overrides the file or env.

The **complete annotated reference** lives at
[`examples/pdbr.example.yaml`](../examples/pdbr.example.yaml). It is
generated from the config structs' `koanf` and `doc` tags
(`pdbr config example`), so it cannot drift from the implementation — never
hand-edit it; regenerate with `make example-config`.

## Environment variable mapping

Strip the `PDBR_` prefix, lowercase, then: `__` becomes a literal `_`, every
remaining `_` becomes a `.` level separator.

| Env var | Key |
|---|---|
| `PDBR_DATABASE_URL` | `database.url` |
| `PDBR_SERVER_MAX__PAGE__SIZE=50` | `server.max_page_size` |
| `PDBR_SERVER_BASE__PATH=/v1` | `server.base_path` |
| `PDBR_RLS_AUTH_JWT__SECRET=...` | `rls.auth.jwt_secret` |
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
| `database.statement_timeout` | duration | `30s` | per-statement timeout on every request (`0` disables); expiry → `504` |
| `server.addr` | string | `:8080` | listen address |
| `server.base_path` | string | `/api` | prefix for every API route; `""` mounts at the root. `/healthz` and `/readyz` are never prefixed |
| `server.openapi` | bool | `true` | serve `GET {base}/openapi.json`; see [openapi.md](openapi.md) |
| `server.request_timeout` | duration | `30s` | overall HTTP request timeout; expiry → `504` problem |
| `server.max_body_bytes` | int | 1048576 | maximum request body size (1 MiB); over → `413` |
| `server.max_embed_depth` | int | 5 | maximum `select=` embed nesting; over → `400 embed_depth_exceeded` |
| `server.max_cost` | int | 10000 | per-request cost limit (1 + Σ list multipliers); over → `400 cost_exceeded`; `@costMultiplier` tunes estimates (see [smart-comments.md](smart-comments.md)) |
| `server.max_page_size` | int | 100 | largest accepted `limit` (larger → `400 page_size_exceeded`); also the default when `limit` is absent; see [pagination.md](pagination.md) |
| `server.cors_origins` | []string | `[]` | allowed CORS origins (exact match, or `*`); empty disables CORS headers and preflight |
| `server.compression` | bool | `false` | gzip responses when the client sends `Accept-Encoding: gzip` |
| `server.read_only` | bool | `false` | every write → `405` (`code: read_only`); runtime-toggleable via `App.SetReadOnly` |
| `server.etags` | bool | `true` | weak `ETag` on single-row GETs, `If-None-Match` → `304`, `If-Match` on `PATCH`/`DELETE` → `412` |
| `server.csv` | bool | `true` | `Accept: text/csv` on lists and set-returning RPCs |
| `schema.schemas` | []string | `[public]` | schema allowlist; resources in the first entry also get bare `/{table}` aliases |
| `schema.cache_path` | string | — | boot from `pdbr schema dump` output; see [caching.md](caching.md) |
| `schema.functions` | bool | `true` | expose functions as `/rpc/{fn}` routes and computed columns ([rpc.md](rpc.md)) |
| `filters.indexed_only` | bool | `true` | only indexed/PK/unique columns filterable and orderable; see [filtering.md](filtering.md) |
| `filters.allow_columns` | map[string][]string | `{}` | per-table extra filterable columns keyed by `schema.table`, overriding `indexed_only` |
| `rls.enabled` | bool | `true` | switch role per request via `SET LOCAL ROLE`; see [rls.md](rls.md) |
| `rls.default_role` | string | — | role for authenticated requests without a role claim |
| `rls.anonymous_role` | string | `anonymous` | role for unauthenticated requests |
| `rls.role_claim` | string | `role` | JWT claim (or header name in headers mode) carrying the role |
| `rls.allowed_roles` | []string | `[]` | roles a request may assume; empty allows any (subject to grants); rejected → `403 role_not_allowed` |
| `rls.claims_prefix` | string | `pdbq.claims` | `set_config` namespace; deliberately shared with pdbq so existing policies work unchanged |
| `rls.auth.mode` | string | `jwt` | `jwt`, `headers` (behind a trusted gateway) or `none` |
| `rls.auth.jwt_secret` | string | — | HMAC secret for HS256/384/512 (ignored when `jwks_url` set) |
| `rls.auth.jwks_url` | string | — | JWKS endpoint for RS*/ES* verification; takes precedence over `jwt_secret` |
| `rls.auth.jwks_cache_ttl` | duration | `1h` | JWKS cache lifetime; unknown `kid` triggers an early refresh |
| `rls.auth.jwt_issuer` | string | — | expected `iss`; empty skips |
| `rls.auth.jwt_audience` | string | — | expected `aud`; empty skips |
| `rls.auth.header_prefix` | string | `X-Pdbr-Claim-` | header prefix mapped to claims in headers mode |
| `rls.auth.trusted_proxies` | []string | `[]` | CIDRs/IPs allowed to supply claim headers; empty trusts every peer (startup warning) |
| `rls.auth.jwt_type` | string | — | schema-qualified composite (e.g. `public.jwt`); functions returning it yield a minted HS256 token |
| `transactions.mutations` | bool | `true` | wrap every write in a tx |
| `transactions.isolation` | string | `read_committed` | or `repeatable_read`, `serializable` |
| `transactions.max_retries` | int | 0 | automatic re-runs on serialization failure/deadlock (40001/40P01); exhausted → `409` |
| `watch.enabled` | bool | `false` | dev only; refuses to combine with cache |
| `watch.poll_interval` | duration | `5s` | fallback polling when event triggers can't be installed |
| `watch.channel` | string | `pdbr_ddl` | NOTIFY channel used by the DDL event trigger |
| `errors.detail` | string | `prod` | `dev` adds full PG detail + `pg` object to problems; `strict` hides even constraint-violation messages |
| `plugins.disabled` | []string | `[]` | e.g. `[nested-mutations]`; see [plugins.md](plugins.md) |
| `plugins.settings.<name>` | map | `{}` | per-plugin config |
| `log.level` | string | `info` | `debug`, `info`, `warn`, `error` |
| `log.format` | string | `text` | `text` or `json` |

## Validation rules

`pdbr config validate` (and every command that loads config) rejects the
effective configuration when any of these fail; all violations are reported
together.

1. `errors.detail` is one of `dev`, `prod`, `strict`.
2. `rls.auth.mode` is one of `jwt`, `headers`, `none`.
3. Every `rls.auth.trusted_proxies` entry is a valid IP or CIDR block.
4. `rls.auth.jwt_secret` or `rls.auth.jwks_url` is set when `rls.enabled`
   and mode is `jwt`.
5. `rls.auth.jwt_type` requires `rls.auth.jwt_secret` (minting signs with
   the HMAC secret; JWKS keys are verify-only).
6. `rls.auth.jwt_type`, when set, is schema-qualified (`public.jwt`).
7. `rls.anonymous_role` is non-empty when `rls.enabled` (an empty role would
   run unauthenticated requests as the connection role).
8. `transactions.isolation` is one of `read_committed`, `repeatable_read`,
   `serializable`.
9. `watch.enabled` and `schema.cache_path` are not both set.
10. `watch.channel` matches `[A-Za-z0-9_]+` when `watch.enabled`.
11. `schema.schemas` lists at least one schema.
12. `transactions.max_retries >= 0`.
13. `server.max_page_size >= 1`.
14. `log.level` is one of `debug`, `info`, `warn`, `error`.
15. `server.base_path` is empty or starts with `/` and does not end with `/`.
16. `server.max_embed_depth >= 1`.

## Removed vs pdbq

Configuration sections are identical to pdbq except `server` and
`transactions`. Keys that exist in pdbq but not in pdbr:

| pdbq key | Why it is gone |
|---|---|
| `server.graphiql` | no embedded UI of any kind; bring your own OpenAPI tooling ([openapi.md](openapi.md)) |
| `server.expose_schema` | replaced by `server.openapi` (on by default) |
| `server.max_depth` | GraphQL selection depth; the REST analogue is `server.max_embed_depth` |
| `server.apq` | persisted queries are a GraphQL concept |
| `server.persisted_queries_path` | same |
| `server.persisted_only` | same |
| `server.disable_introspection` | GraphQL introspection; the REST analogue is `server.openapi: false` |
| `transactions.per_request` | a REST request is exactly one operation, so "one tx per request" and "one tx per operation" coincide |

## Flags

Flags mirror koanf paths, so precedence needs no mapping table. Every
subcommand that loads configuration accepts them plus `--config`.

| Flag | Type | Notes |
|---|---|---|
| `--config` | string | path to config file (default: `./pdbr.yaml` / `./pdbr.yml` if present) |
| `--database.url` | string | |
| `--server.addr` | string | |
| `--server.base_path` | string | |
| `--server.openapi` | bool | |
| `--server.cors_origins` | []string | comma-separated or repeated |
| `--server.compression` | bool | |
| `--server.read_only` | bool | |
| `--schema.schemas` | []string | comma-separated or repeated |
| `--schema.cache_path` | string | |
| `--rls.enabled` | bool | |
| `--watch.enabled` | bool | |
| `--filters.indexed_only` | bool | |
| `--errors.detail` | string | `dev`, `prod`, `strict` (shell-completed) |
| `--log.level` | string | `debug`, `info`, `warn`, `error` (shell-completed) |

Keys without a flag are still settable through YAML or `PDBR_*` env vars.
