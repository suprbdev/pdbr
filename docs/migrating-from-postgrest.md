# Migrating from PostgREST

pdbr is a PostgREST-style server: it introspects PostgreSQL, generates REST
routes and follows PostgREST's URL grammar and `Prefer` vocabulary where it
can. It is not a drop-in replacement, so a few behaviors differ and a few
features are missing. This guide is for people moving a PostgREST v12 or v13
deployment. It lists what maps directly, what differs and what is missing, then
a migration order.

> **Re-check against the version you deploy.** This guide describes pdbr 1.0.
> The URL grammar, error codes, configuration keys and CLI are covered by the
> [1.0 compatibility promise](compatibility.md); check
> [upgrading.md](upgrading.md) for what changed from 0.x and re-run your
> client tests against the version you deploy. The deliberate departures are
> also listed in [features.md](features.md#differences-from-postgrest).

Two facts shape everything else: pdbr compiles each request to **one SQL
statement** that returns the response JSON, and it is a single Go binary
configured by YAML, environment variables and flags rather than a PostgREST
config file.

## URLs and reads

Maps directly:

- `/{table}` and `/rpc/{function}`, filters `col=op.value` with `eq`, `neq`,
  `gt`, `gte`, `lt`, `lte`, `like`, `ilike`, `in`, `is`, `cs`, `cd`, `ov`, the
  full-text operators and `not.`, `or=(...)`, `and=(...)`, and `any` / `all`
  modifiers ([filtering.md](filtering.md)).
- `select=` with column lists, aliases, casts, embedded resources (`posts(title)`),
  `!inner`, spreads (`...posts(title)`), JSON paths (`settings->>theme`) and
  aggregate functions ([api.md](api.md), [aggregates.md](aggregates.md)).
- `order=col.desc.nullsfirst`, `limit`, `offset`, `Range`/`Content-Range`,
  `Prefer: count=exact|planned|estimated`, `HEAD`, CSV output (`Accept: text/csv`).
- JSON bodies for `POST`, `PATCH`, `PUT` and `DELETE`, `Prefer: return=`,
  `Prefer: resolution=merge-duplicates|ignore-duplicates`, `on_conflict=`,
  `columns=`, `Prefer: max-affected=` (with `handling=strict`, as in
  PostgREST) and `Prefer: tx=rollback` (needs `transactions.end:
  commit-allow-override` or `rollback-allow-override`, as `db-tx-end` does).
- `GET` for stable and immutable RPC functions, `POST` for volatile ones, named
  arguments, computed columns (functions taking a row type).

Differences:

| Area | PostgREST | pdbr |
|---|---|---|
| Base path | `/` | `server.base_path` defaults to `""` (the root); set it to e.g. `/api` to prefix routes |
| Schemas | `Accept-Profile` / `Content-Profile` headers choose the schema | The path does: `/{schema}/{table}`; the bare form is the first entry of `schema.schemas`. The profile headers are ignored |
| `limit` above the cap | clamped to `db-max-rows` | `400 page_size_exceeded` when `limit` exceeds `limits.max_page_size` (100 by default); a `Range` header is clamped |
| Default page | unbounded unless `db-max-rows` is set | a list without `limit` returns at most `limits.max_page_size` rows, with `Link` and `X-Next-Cursor` headers to continue |
| Pagination | `limit`/`offset`/`Range` | also keyset cursors (`after=`, `before=`), `X-Total-Count` next to `Content-Range`; a keyset page has no `Content-Range` |
| Ordering | PostgreSQL's default: `NULLS FIRST` for `DESC` | `NULLS LAST` for both directions unless `nullsfirst` / `nullslast` is given |
| Casts | any type | an allowlist of built-in scalar types and the API's enums; never `reg*`, pseudo-types or composites |
| `count=planned` | `EXPLAIN` estimate | `pg_class.reltuples`, only for unfiltered scans of tables without RLS; otherwise falls back to `exact` and `Preference-Applied` says so |
| Duplicate response keys | both keys are returned | `400 invalid_select` unless the same column is selected twice |
| Filterable columns | all | only indexed columns by default (`filters.indexed_only: true`; `filters.allow_columns` and `@filterable` as exceptions), see [filtering.md](filtering.md) |
| pdbr-only names | — | everything pdbr adds beyond PostgREST's grammar that could collide with a column starts with `~`: reference parameters (`~near=`, `~vec=`, `~trgm=`, …) and derived values (`select=~location_area,~emb_cosine_distance`); they never shadow your columns, see [api.md](api.md#the--namespace) |

Missing today (a request for these is a `400`, `406` or `415`, not a silent
fallback):

- `!left` embed hints (`!inner` and FK-name or column hints work).
- `is.unknown` (`is` takes `null`, `true` or `false`).
- `Accept: application/vnd.pgrst.object+json`, `application/vnd.pgrst.array+json`
  and `application/vnd.pgrst.plan`. Use `/by/{cols}/{vals}` or `/{table}/{pk}`
  for a single object.
- `text/csv` and form-encoded request bodies; bodies are JSON only.
- Limited updates and deletes (`PATCH`/`DELETE` with `limit` and `order`), and
  writes to views: pdbr treats views and materialized views as read-only.
- RPC: `Prefer: params=single-object`, a single unnamed `json` parameter taking
  the whole body, and array bodies for bulk calls.

## Responses and errors

- **Errors are RFC 9457 problem details**, `application/problem+json`, with a
  stable `code` from a closed vocabulary (`unknown_column`,
  `page_size_exceeded`, `pg:23505`, ...), not `PGRSTxxx` codes and not
  PostgREST's `{code, message, details, hint}` body. Grammar codes name the
  part of the request at fault: `invalid_filter` (a filter),
  `invalid_parameter` (`limit`, `order`, other reserved parameters, a path
  key), `invalid_select`, `invalid_argument` (an RPC or computed-column
  argument) and `invalid_body`. A hint that selects no relation, such as
  PostgREST's `!left`, is `unknown_relation`.
  Database errors carry `pg:<SQLSTATE>`; with `errors.detail: dev` a `pg`
  member adds the message, detail, hint, table and constraint. Map your client's
  error handling using [errors.md](errors.md).
- **Writes return the representation by default.** PostgREST defaults to
  `return=minimal`. Send `Prefer: return=minimal` to get `201`/`204` without a
  body; `return=headers-only` is an alias.
- **Unfiltered `PATCH` and `DELETE` are refused** (`400 unfiltered_mutation`)
  unless `mutations.allow_unfiltered: true`, as with the `pg-safeupdate`
  extension.
- **`PUT` on a row path (`/users/1`) is a partial upsert.** PostgREST needs every
  column in the body; pdbr keeps the columns you do not send on update. The
  statement is still an insert-or-update, so a `NOT NULL` column without a
  default must be in the body (`400 invalid_body` otherwise).
- **`Prefer: missing=default` is a no-op**: pdbr always inserts `DEFAULT` for
  omitted keys in bulk bodies.
- **ETags** are weak and computed in SQL on single-row reads
  (`W/"<row>.<repr>"`: the row state and the response body), with
  `If-None-Match` answering `304` and `If-Match` available for optimistic
  writes against the row part ([mutations.md](mutations.md)). With `server.etags: false`, `If-Match` is
  `400 if_match_unsupported`.
- **Bulk writes may use `Prefer: partial=items`** to report per-element results
  (experimental, [compatibility.md](compatibility.md#experimental-features)).
- **Identity `BY DEFAULT` columns are writable**; identity `ALWAYS` and generated
  columns are read-only.

## Authentication and RLS

Maps directly: a bearer JWT with a role claim (`role` by default), an anonymous
role, `SET LOCAL ROLE` per request and the same RLS policies. HS256/384/512
secrets and JWKS (RS*/ES*) verification are supported, as are audience and
issuer checks.

| PostgREST | pdbr |
|---|---|
| `jwt-secret` | `auth.jwt.secret` (or `auth.jwt.secret_file`, `auth.jwt.jwks_url`) |
| `jwt-aud` | `auth.jwt.audience` |
| `jwt-role-claim-key` | `auth.jwt.role_claim_path`: dot-separated keys with array indexes and quoted keys (`app_metadata.role`, `realm_access.roles[0]`), without PostgREST's leading dot |
| `db-anon-role` | `roles.anonymous` (empty by default: no credentials is a `401`) |
| `request.jwt.claims` setting | set `claims.json_setting: request.jwt.claims` |
| `request.jwt.claim.<name>` settings | `jwt.claims.<name>` (`claims.prefix`, default `jwt.claims`; set `claims.prefix: request.jwt.claim` to keep PostgREST's per-claim names) |
| Role switch | `roles.switch: true` (default) |

Differences to plan for:

- **Claims as settings.** Policies written for PostgREST that read
  `current_setting('request.jwt.claims', true)::json->>'sub'` work once
  `claims.json_setting: request.jwt.claims` is set. Policies reading the
  per-claim settings need `claims.prefix` set to their name, or an update to
  `jwt.claims.<name>`.
- **Tokens must carry `exp`.** A token without it is rejected.
- **Anonymous access is opt-in.** Set `roles.anonymous` to your `db-anon-role`.
- **No request metadata settings.** `request.headers`, `request.cookies`,
  `request.method`, `request.path`, and the response-side `response.headers` and
  `response.status`, are not set. Policies or functions that read them need a
  rewrite, or a `RequestHook` Go plugin (experimental), or a gateway that turns
  them into claims (`auth.mode: headers`).
- **`db-pre-request`** has no equivalent besides a `RequestHook` plugin.
- **The authenticator role must inherit the request roles.** pdbr introspects
  with the connection role's own privileges, so a `NOINHERIT` authenticator
  (PostgREST's recommendation) sees no tables. Create it with the default
  `INHERIT`, as in [Secure setup](quickstart.md#secure-setup). On PostgreSQL 16
  and later the inherit flag is recorded per membership when the role is
  granted, so `ALTER ROLE authenticator INHERIT` does not change existing
  grants: re-run `GRANT <request role> TO authenticator WITH INHERIT TRUE`.
  Or dump a schema cache as a privileged role and boot from it
  ([caching.md](caching.md)).
- Set `roles.allowed` to the roles tokens may name.

## OpenAPI

PostgREST serves Swagger 2 at `/`. pdbr serves **OpenAPI 3.1 at
`{base_path}/openapi.json`** (`openapi.enabled`, default on), deterministic and
pinned by golden tests. There is no `follow-privileges` mode; the document
describes what the connection role can reach. Set `openapi.title` and
`openapi.server_url` for proxies. There is no embedded UI: bring your own.

## Configuration mapping

| PostgREST | pdbr |
|---|---|
| `db-uri` | `database.url` (`PDBR_DATABASE_URL`) |
| `db-schemas` | `schema.schemas` |
| `db-anon-role` | `roles.anonymous` |
| `db-pool` | `database.max_conns` |
| `db-max-rows` | `limits.max_page_size` (an error above it, see above) |
| `db-tx-end` | `transactions.end` (`commit`, `commit-allow-override`, `rollback`, `rollback-allow-override`; applies to writes) |
| `db-aggregates-enabled` | `aggregates.enabled` |
| `jwt-secret`, `jwt-aud`, `jwt-role-claim-key` | `auth.jwt.secret`, `auth.jwt.audience`, `auth.jwt.role_claim_path` |
| `server-host` / `server-port` | `server.addr` (`:8080`) |
| `server-cors-allowed-origins` | `server.cors_origins` |
| `openapi-mode` | `openapi.enabled` |
| `openapi-server-proxy-uri` | `openapi.server_url` |
| `log-level` | `log.level` (`log.format: text` or `json`) |
| `admin-server-port` | `/healthz` and `/readyz` on the main listener; `metrics.addr` for a separate Prometheus listener |
| `NOTIFY pgrst, 'reload schema'` | `watch.enabled` (a DDL event trigger, development) or restart; `pdbr schema check` is a CI drift gate |
| `db-pre-request`, `app.settings.*`, `db-extra-search-path`, `db-prepared-statements` | no equivalent |

Every key is also an environment variable (`PDBR_...`, `.` becomes `_` and `_`
within a key is written `__`, see [configuration.md](configuration.md)) and a
flag. Configuration is strict: unknown YAML keys and misspelt `PDBR_*`
variables are errors that suggest the closest key.

## Operations

- pdbr is one static Go binary or a distroless image. `GET /healthz` and
  `GET /readyz` are the probes and live at the root, not under the base path.
- `pdbr request GET '/users?select=id'` runs a request without a server, and
  `--print-sql` shows the statement it compiles to.
- Boot from a schema cache (`pdbr schema dump`, `schema.cache_path`) to avoid
  catalog access at runtime; `pdbr schema check` fails CI when it drifts.
- `/metrics` is available with `metrics.enabled` ([metrics.md](metrics.md)).
- Errors, casts and filters are stricter than PostgREST's, so run your client
  suite against pdbr before switching traffic.

## Migration order

1. Run pdbr next to PostgREST against the same database with
   `filters.indexed_only: false`, `server.base_path` set to what your clients use
   (or leave it empty) and `claims.json_setting: request.jwt.claims`.
2. Map `db-anon-role`, JWT and `db-schemas` settings with the table above, and
   create the authenticator role with `INHERIT`.
3. Replay your client's requests, comparing status codes and bodies. Fix error
   handling for the problem-details shape and switch writes that depend on
   `return=minimal` to send the preference.
4. Check the missing features above against your clients: object `Accept`
   headers, `!left`, limited updates, `Accept-Profile`.
5. Tighten: restore `filters.indexed_only: true` and add the indexes your
   filters need, or opt columns in with `@filterable`; set `roles.allowed`;
   keep `limits.max_page_size` and `limits.max_cost` at sensible values.
6. Switch traffic.
