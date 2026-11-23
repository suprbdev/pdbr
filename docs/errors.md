# Errors

As built (M6). Every non-2xx/3xx response is an
[RFC 9457](https://www.rfc-editor.org/rfc/rfc9457) Problem Details document,
`Content-Type: application/problem+json`, regardless of `Accept`. Every row
of the tables below that the fixture database can reach has one assertion in
`test/e2e_errors_test.go` (rows it cannot reach are marked *unreachable in
e2e*).

## Format

```http
HTTP/1.1 409 Conflict
Content-Type: application/problem+json

{"type":"about:blank","title":"Conflict","status":409,"detail":"duplicate key value violates unique constraint \"users_email_key\"","instance":"/api/users","code":"pg:23505"}
```

| Member | Value |
|---|---|
| `type` | always `about:blank` (v1: no problem-type URIs; `code` is the machine-readable discriminator) |
| `title` | the HTTP reason phrase (`Bad Request`, `Conflict`, …) |
| `status` | the HTTP status, repeated |
| `detail` | human-readable message, subject to [`errors.detail`](#errorsdetail) sanitization |
| `instance` | request path without query string (`/api/users/1`) |
| `code` | extension: stable string — `pg:<SQLSTATE>` for database errors, a pdbr code otherwise ([vocabulary](#code-vocabulary)) |
| `pg` | extension, **`errors.detail: dev` only**: `{"sqlstate","message","detail","hint","table","constraint"}` (absent members omitted) |

A grammar error rendered by pdbr itself:

```json
{"type":"about:blank","title":"Bad Request","status":400,"detail":"unknown column \"emial\" on \"users\"","instance":"/api/users","code":"unknown_column"}
```

`HEAD` errors carry the same status/headers and no body. Problem bodies are
never gzip-compressed differently from normal responses (`server.compression`
applies uniformly).

## `code` vocabulary

pdbr codes (from the brief; the parenthesised codes are additions where the
brief needed one but named none — kept as separate codes so every response
has a `code`, see the note below the table):

| `code` | Status | Meaning |
|---|---|---|
| `not_found` | 404 | no such route/resource/function, or single-row path matched zero rows (incl. rows hidden by RLS), or a nested `connect` found no parent |
| `invalid_filter` | 400 | filter grammar: bad `op`, unparsable value, bad `and=`/`or=` nesting, missing `near=` with distance order, bad `in.(…)` list, uncoercible RPC GET arg |
| `invalid_select` | 400 | `select=` grammar: unbalanced parens, empty item, bad alias, unknown hint, `col(k=v)` args on a non-spatial column; aggregates ([aggregates.md](aggregates.md#errors)): a cast outside an aggregate or to a type outside the allowlist (`::regclass`), an aggregate without the input type (`text.sum()`), an embed or ungroupable column as a grouping key, duplicate response keys, aggregates on single-row paths, mutations or in to-one embeds, `distinct=` with aggregates |
| `aggregates_disabled` | 400 | an aggregate function in `select=` while `server.aggregates` is off (PostgREST `PGRST123`) |
| `unknown_column` | 400 | column / computed name not on the resource — in `select=`, filters, `order=`, `distinct=`, `on_conflict=` |
| `column_not_filterable` | 400 | column exists but is outside the filter policy (`filters.indexed_only`, `@omit filter`) |
| `column_not_orderable` | 400 | column exists but is outside the order policy; with aggregates, an `order=` term that is not a grouping column |
| `page_size_exceeded` | 400 | `limit` > `server.max_page_size` (also on embeds and RPC lists) |
| `keyset_unsupported` | 400 | `after`/`before` on a resource without a PK, `before` combined with `offset`, any cursor combined with a `Range` header, `distinct=` or aggregate functions with keyset |
| `invalid_range` | 400 | `Range` header with the last item before the first, or no overlap with `offset`/`limit` ([pagination.md](pagination.md#range-request-header)) |
| `range_not_satisfiable` | 416 | counted list whose `offset` / `Range` start is past the total; carries `Content-Range: */<total>` (PostgREST `PGRST103`) |
| `cost_exceeded` | 400 | estimated cost > `server.max_cost` |
| `embed_depth_exceeded` | 400 | `select=` nesting deeper than `server.max_embed_depth`; nested-mutation depth > `max_depth` |
| `ambiguous_relation` | 400 | relation key matches ≥ 2 FKs and no `!hint`; detail lists the candidates |
| `invalid_body` | 400 | body not an object/array as required, unknown key, empty patch, missing required columns, missing conflict-target columns, both `create` and `connect`, uncoercible value, empty `[]` bulk, single-row `PUT` body key ≠ path key (PostgREST `PGRST115`) |
| `temporal_conflict_target` | 400 | `PUT` or `POST` with `Prefer: resolution=merge-duplicates` whose conflict target is a temporal (`WITHOUT OVERLAPS`, PostgreSQL 18) key — `?on_conflict=` naming one, or the default target on a table whose primary key is temporal (`ignore-duplicates` accepts them); `ON CONFLICT` cannot infer the exclusion constraint behind it (PostgreSQL would fail with `42P10`) |
| `column_readonly` | 400 | generated / identity-`ALWAYS` column in a write body |
| `read_only` | 405 | write attempted while `server.read_only` (config or `App.SetReadOnly`) |
| `etag_mismatch` | 412 | `If-Match` did not match the current row hash |
| (`unfiltered_mutation`) | 400 | bulk `PATCH`/`DELETE` on a collection path without a root filter or `!inner` embed while `server.allow_unfiltered_mutations` is off |
| (`if_match_unsupported`) | 400 | `If-Match` on a write that cannot honour it (bulk `PATCH`/`DELETE` on a collection path, `PUT` on a single-row path) while `server.etags` is on |
| (`invalid_preference`) | 400 | `Prefer: handling=strict` with an unknown token or a malformed value (PostgREST `PGRST122`); `resolution=merge-duplicates` on a resource without `PUT` |
| (`max_affected_exceeded`) | 400 | `Prefer: handling=strict, max-affected=N` and the write touched more than `N` rows; rolled back (PostgREST `PGRST124`) |
| `not_acceptable` | 406 | `Accept` unsatisfiable (incl. `text/csv` with embeds or on non-list responses) |
| `unsupported_media_type` | 415 | request body `Content-Type` is not `application/json` |
| `role_not_allowed` | 403 | credential valid, but the resolved role is not in `rls.allowed_roles`, or is `none`, `public`, a `pg_*` role or not `[A-Za-z0-9_]+` |
| `invalid_token` | 401 | JWT/headers present but invalid (signature, exp, iss/aud, JWKS miss or unavailable, malformed; a claim header misspelt, repeated, folding onto another claim or outside `rls.auth.header_claims`) |
| (`unknown_relation`) | 400 | relation key in `select=`, a filter/order path, a per-embed param, or a nested-mutation body that matches no FK of the resource |
| (`invalid_cursor`) | 400 | `after`/`before` cursor undecodable or minted for a different resource |
| (`method_not_allowed`) | 405 | method not registered on the path (view, missing privilege, `@omit`, volatile GET) |
| (`unknown_argument`) | 400 | RPC argument name not declared by the function |
| (`missing_argument`) | 400 | RPC call omits an argument that has no `DEFAULT` (or, on a variadic function, one before a passed argument); detail lists them |
| (`body_too_large`) | 413 | body > `server.max_body_bytes` |
| (`timeout`) | 504 | `server.request_timeout` elapsed (context deadline) |
| (`unavailable`) | 503 | no database connection (pool acquire failed, connection refused/reset/dropped, `net.Error`, unexpected EOF) with no SQLSTATE; `detail` "database unavailable" (full error in `dev`) |
| (`internal`) | 500 | anything else pdbr-side (hook error, unexpected compile failure, panic) |
| `pg:<SQLSTATE>` | per table below | database error; the five-character SQLSTATE verbatim (`pg:23505`, `pg:42501`, `pg:40001`) |

Decided (M6): the parenthesised codes stay as distinct codes — the
minimum needed so every response has a `code` (`unknown_relation` and
`invalid_cursor` are also used by [mutations.md](mutations.md) /
[pagination.md](pagination.md)); folding them into neighbours
(`unknown_column`, `keyset_unsupported`, `invalid_body`) or emitting no
`code` on 405/413/500/503/504 was rejected because clients branch on `code`.
The PostgREST-parity codes (`unfiltered_mutation`, `invalid_preference`,
`max_affected_exceeded`, `if_match_unsupported`) were added later under the
same rule.

Codes are stable across releases; new codes may be added in minor versions,
existing ones are never renamed.

## Status mapping

Everything pdbr renders passes through one mapping (grammar/plan errors are
typed Go errors; database errors go through `pdbcore/pgexec.Classify`, which
returns `{Class, SQLState, Message(detail)}`):

| Condition | Status | `code` |
|---|---|---|
| URL grammar, query param, body validation errors, unfiltered bulk write, invalid `Prefer` under `handling=strict`, `max-affected` exceeded | 400 | pdbr code |
| `22*` data exception (`22P02` invalid text representation, `22001` value too long, `22003` out of range, …) | 400 | `pg:22…` |
| `23502` not-null violation | 400 | `pg:23502` |
| `23514` check violation | 400 | `pg:23514` |
| `42*` other than `42501` (undefined function/column, syntax — should not happen; message "invalid operation" in prod/strict) | 400 | `pg:42…` |
| malformed full-text / ltree operand (`pdbcore/pgexec` contrib input errors): `42601` raised while parsing a `tsquery`/`tsvector`/`ltree`/`lquery`/`ltxtquery` value (`syntax error in tsquery: …`, `ltree syntax error …`, …), `42704` unknown text search configuration | 400 | `pg:42601` / `pg:42704` |
| PostGIS input error (`XX000` with `Operation on mixed SRID geometries`, `parse error - invalid geometry` or an `lwgeom_*` message): an operand or body value PostGIS rejects | 400 | `pg:XX000` |
| missing or invalid credentials when the request cannot be served anonymously; invalid token | 401 | `invalid_token` |
| `42501` insufficient privilege (incl. RLS `RETURNING` filtering) | 403 | `pg:42501` |
| resolved role not in `rls.allowed_roles`, or not assumable (`none`, `public`, `pg_*`) | 403 | `role_not_allowed` |
| no route; single-row path or keyed mutation matched nothing | 404 | `not_found` |
| method not on resource / read-only / volatile via GET | 405 + `Allow` | `method_not_allowed` / `read_only` |
| `Accept` unsatisfiable | 406 | `not_acceptable` |
| `23505` unique violation, `23503` FK violation, `23001` restrict violation (PostgreSQL 18+ reports `ON DELETE/UPDATE RESTRICT` as `23001`, older servers as `23503`), `23P01` exclusion violation (also a temporal `WITHOUT OVERLAPS` key duplicate) | 409 | `pg:235…` / `pg:23001` / `pg:23P01` |
| `40001` serialization failure / `40P01` deadlock **after** `transactions.max_retries` re-runs | 409 | `pg:40001` / `pg:40P01` |
| `If-Match` mismatch | 412 | `etag_mismatch` |
| body larger than `server.max_body_bytes` | 413 | `body_too_large` |
| body `Content-Type` not JSON | 415 | `unsupported_media_type` |
| any other PG SQLSTATE (`21000`, `2D000`, `P0001` `RAISE EXCEPTION`, `XX000` other than the PostGIS input errors above, …) | 500 | `pg:<SQLSTATE>` |
| hook / plugin / internal error that is not a `UserError` | 500 | `internal` |
| `53*` insufficient resources (`53300` too many connections, `53200` OOM), `08*` connection exceptions (*unreachable in e2e*), pool acquire / connection failure without a SQLSTATE | 503 | `pg:53…` / `pg:08…` / `unavailable` |
| `57014` statement timeout (`database.statement_timeout`), `server.request_timeout` elapsed | 504 | `pg:57014` / `timeout` |

`P0001` (`RAISE EXCEPTION` from user PL/pgSQL) is deliberately 500 with a
generic message in prod: user-raised messages are not classified as
client-safe.

Decided (M6): PostgREST maps `P0001` to 400 and lets `RAISE … USING ERRCODE`
pick the status; pdbr keeps 500 (pdbq parity). A `RAISE … USING ERRCODE`
that names a classified state (`23514`, `22023`, `42501`, …) still gets that
state's status because the mapping is by SQLSTATE, so functions can already
signal client errors that way. An `errors.raise_passthrough` knob for
`P0001` remains a possible future option.

## `errors.detail`

`errors.detail: dev | prod | strict` (default `prod`) controls **only the
message text and the `pg` object**; status and `code` never change with the
level, so clients can branch on `code` in every environment.

| Level | `detail` for PG errors | `pg` object |
|---|---|---|
| `dev` | full PG message | present |
| `prod` (default) | `23*` and `22*` messages pass through, and so do contrib input errors (`syntax error in tsquery: …`); PostGIS input errors → `invalid geometry`; `42501` → `permission denied`; other `42*` → `invalid operation`; everything else → `database error` | absent |
| `strict` | `23*` → `constraint violation`; `22*`, contrib and PostGIS input errors → `invalid input value`; rest as `prod` | absent |

Sanitization mirrors pdbq's `sanitizePGMessage`: constraint (`23*`) and
data-exception (`22*`) messages are user-actionable and safe; `42*` messages
can leak schema internals; everything else is hidden. pdbr's own grammar
errors (`unknown_column`, `invalid_filter`, …) are always rendered in full —
they only echo what the client sent plus names already visible in the
OpenAPI document.

The same unique-violation error at the three levels:

```json
// errors.detail: dev
{"type":"about:blank","title":"Conflict","status":409,
 "detail":"duplicate key value violates unique constraint \"users_email_key\"",
 "instance":"/api/users","code":"pg:23505",
 "pg":{"sqlstate":"23505","message":"duplicate key value violates unique constraint \"users_email_key\"",
       "detail":"Key (email)=(a@b.c) already exists.","table":"users","constraint":"users_email_key"}}
```

```json
// errors.detail: prod
{"type":"about:blank","title":"Conflict","status":409,
 "detail":"duplicate key value violates unique constraint \"users_email_key\"",
 "instance":"/api/users","code":"pg:23505"}
```

```json
// errors.detail: strict
{"type":"about:blank","title":"Conflict","status":409,
 "detail":"constraint violation",
 "instance":"/api/users","code":"pg:23505"}
```

And a `RAISE EXCEPTION 'nope'` from a function (`P0001`):

| Level | `detail` |
|---|---|
| `dev` | `nope` (+ `pg` object with `hint`/`detail` if raised with `USING`) |
| `prod` | `database error` |
| `strict` | `database error` |

Every 5xx is logged at `error` with the unsanitized message and the request
method/path (never the body or claims), so `prod` loses nothing operationally.

## Retries

With `transactions.max_retries: N` (default 0), a statement failing with
`40001` or `40P01` is re-run up to N more times: the failed transaction is
rolled back, a new one begins (same isolation, `SET LOCAL ROLE`, claims), the
same SQL and parameters execute. No backoff, no jitter (mirror pdbq). Reads
are retried too when they run in a transaction. Exhaustion → `409` with
`code: pg:40001` / `pg:40P01`, `detail` "database error" in prod/strict (class
`40` is not pass-through), full message in dev. Retry count is logged at
`debug`.

Retries never apply to any other class — a `23505` inside a
`serializable` transaction is final on the first attempt.

## `405` and `Allow`

Every `405` carries `Allow` listing the methods actually registered on that
path (routes are a pure function of catalog + config + plugins, so `Allow` is
static per path until a hot swap):

| Path | Typical `Allow` |
|---|---|
| `/{table}` (full privileges) | `GET, HEAD, POST, PUT, PATCH, DELETE` (+ `OPTIONS` when `server.cors_origins` set) |
| `/{table}` (view) | `GET, HEAD` |
| `/{table}/{pk}` (identity / generated key) | `GET, HEAD, PATCH, DELETE` |
| `/{table}/{pk}`, `/{table}/by/{cols}/…` (writable key) | `GET, HEAD, PUT, PATCH, DELETE` |
| `/{table}/{pk}` on a table without UPDATE grant (*unreachable in e2e*: the fixture's connection role owns everything) | `GET, HEAD, DELETE` |
| `/rpc/{stable_fn}` | `GET, HEAD, POST` |
| `/rpc/{volatile_fn}` | `POST` |
| anything under `server.read_only` | writes removed from the list; `code: read_only` when the request was a write that would otherwise exist, `method_not_allowed` otherwise. A volatile function's route keeps an **empty** `Allow` (RFC 9110 §10.2.1: no method is available) |
| `/openapi.json`, `/healthz`, `/readyz` | `GET, HEAD` (problem+json like every other 405, not the mux's text/plain) |

`OPTIONS` on a known path with CORS enabled is a preflight (`204`), never a
`405`. Unknown path → `404`, not `405`.

## `406` and `415`

- `415 unsupported_media_type`: `POST`/`PATCH`/`PUT` with a body whose
  `Content-Type` is missing or not `application/json` (parameters such as
  `charset=utf-8` are allowed). `DELETE` and RPC `POST` without a body skip
  the check.
- `406 not_acceptable`: `Accept` present and neither `application/json`,
  `application/problem+json`, `*/*`, `application/*` nor (where supported)
  `text/csv` is acceptable. `text/csv` is supported on list GETs and
  set-returning RPC GETs only; requesting it elsewhere, or with embeds in
  `select=`, or with `server.csv: false` → `406`. Error bodies are always
  `application/problem+json` even when the client only accepted `text/csv`.

## `412`

`If-Match` on `PATCH`/`DELETE` single-row paths ([mutations.md](mutations.md#if-match-on-patch--delete)):
row exists and hash differs → `412 etag_mismatch`, transaction rolled back,
no body change. Row missing → `404` (takes precedence over `412`).
`If-Match` on bulk `PATCH`/`DELETE` and on a single-row `PUT` is
`400 if_match_unsupported`; on other routes it is ignored. `If-None-Match` on single-row `GET`
match → `304` (no body, not a problem document).

## `413`

Request bodies are read through a `MaxBytesReader` at
`server.max_body_bytes` (1 MiB); overflow → `413 body_too_large` before JSON
decoding, connection closed after the response. `Content-Length` larger than
the limit short-circuits without reading.

## `504` vs `503`

| Cause | Status | `code` |
|---|---|---|
| `database.statement_timeout` fired inside PG (`57014`) | 504 | `pg:57014` |
| `server.request_timeout` fired in Go (context deadline; the statement is cancelled) | 504 | `timeout` |
| PG refused/dropped the connection (`08*`), pool acquire failed or timed out (`database.connect_timeout`), `53*` resource errors, `57P01` admin shutdown | 503 | `pg:08…` / `pg:53…` / `unavailable` (`detail` "database unavailable" outside `dev`) |

Rule of thumb: the request was *too slow* → 504; the database was *not
available* → 503 (safe to retry after a delay; every 503 problem carries
`Retry-After: 1`). `GET /readyz` answers 503 `unavailable` when the pool ping fails
within `server.readyz_timeout`; `/healthz` stays a liveness check
([deployment.md](deployment.md#docker-image)).

## Hook and plugin errors

Errors returned by `RequestHook.BeforeRequest`, `CompileHook`s, or the
compiler itself are rendered by one rule (mirror of pdbq's
`compile.UserError` passthrough):

- Errors typed as `UserError` (pdbr's `compile.UserError`, every `parse`
  error, and any hook error wrapped as one) are client-facing:
  `400` with the message as `detail` and the code the error carries
  (default `invalid_body` for body-related, `invalid_filter` otherwise).
- Any other error → `500 internal`, `detail: "internal error"` in `prod`
  and `strict`, the full `err.Error()` in `dev`. Always logged at `error`.
- `AfterRequest` errors are logged only; the response is already committed.
- Compile errors that are not `UserError` (a bug: e.g. plugin produced a
  plan the compiler can't handle) render as `500 internal` with
  `detail: "compile: invalid request"` in prod — never the raw message, which
  can carry table/column/type names.

Fuzz tests (`FuzzParse`) assert that every error out of the URL/body parser is
a `UserError`, so a `500` from parsing is by construction a bug.

## Related

- [mutations.md](mutations.md) — 404/412 on keyed writes, body validation
  errors, transaction and retry behaviour for writes.
- [rpc.md](rpc.md) — 405 on volatile GET, argument errors.
- [rls.md](rls.md) — 401/403, `role_not_allowed`.
- [configuration.md](configuration.md) — `errors.detail`, `transactions.*`,
  `server.request_timeout`, `database.statement_timeout`.
