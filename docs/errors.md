# Errors

Every non-2xx/3xx response is an
[RFC 9457](https://www.rfc-editor.org/rfc/rfc9457) Problem Details document,
`Content-Type: application/problem+json`, regardless of `Accept`. Every row
of the tables below that the fixture database can reach has one assertion in
`test/e2e_errors_test.go` (rows it cannot reach are marked *unreachable in
e2e*).

## Format

```http
HTTP/1.1 409 Conflict
Content-Type: application/problem+json

{"type":"about:blank","title":"Conflict","status":409,"detail":"duplicate key value violates unique constraint \"users_email_key\"","instance":"/users","code":"pg:23505"}
```

| Member | Value |
|---|---|
| `type` | always `about:blank` (v1: no problem-type URIs; `code` is the machine-readable discriminator) |
| `title` | the HTTP reason phrase (`Bad Request`, `Conflict`, …) |
| `status` | the HTTP status, repeated |
| `detail` | human-readable message, subject to [`errors.detail`](#errorsdetail) sanitization |
| `instance` | request path without query string (`/users/1`) |
| `code` | extension: stable string — `pg:<SQLSTATE>` for database errors, a pdbr code otherwise ([vocabulary](#code-vocabulary)) |
| `pg` | extension, **`errors.detail: dev` only**: `{"sqlstate","message","detail","hint","table","constraint"}` (absent members omitted) |

A grammar error rendered by pdbr itself:

```json
{"type":"about:blank","title":"Bad Request","status":400,"detail":"unknown column \"emial\" on \"users\"","instance":"/users","code":"unknown_column"}
```

`HEAD` errors carry the same status/headers and no body. Problem bodies are
never gzip-compressed differently from normal responses (`server.compression`
applies uniformly).

## `code` vocabulary

The vocabulary is closed: every problem pdbr renders carries exactly one of
the codes below, or `pg:<SQLSTATE>` for a database error, and no other.
`code` and `status` do not depend on [`errors.detail`](#errorsdetail), so
clients can branch on `code` in every environment. A code keeps its name and
meaning across 1.x releases; a release that adds one (for a feature no
existing code describes) lists it in [upgrading.md](upgrading.md). Treat a
code you do not know as the generic error of its status.

The codes for concepts pdbq shares are spelled the same in both products:
`invalid_token`, `auth_required`, `role_not_allowed`, `read_only`,
`cost_exceeded`, `depth_exceeded`, `page_size_exceeded`, `not_found`,
`timeout`, `unavailable` and `pg:<SQLSTATE>`.

### Request grammar (400)

Each code names the part of the request at fault: a filter, `select=`, a
reserved query parameter, a function or computed-column argument, or the
body.

| `code` | Meaning |
|---|---|
| `invalid_filter` | a filter (`col=op.value`, `and=`/`or=`/`not.` logic, relation and embed filters): unknown or inapplicable operator (`eq` on a spatial, vector, cube or earth column, a spatial operator on a non-spatial column, anything but `st_intersects`/`is` on a raster column), malformed operand, list, quoting, `any`/`all` modifier or text search configuration, a value that does not coerce to the column's type (`id=eq.abc`), a non-numeric distance threshold, bad relation-filter grammar (quantifier on a to-one relation, `exists` misuse, a path deeper than one level), relation filters while the advanced-filters plugin disables them, filters on a write that takes none (`POST`, `PUT`, `PATCH`/`DELETE` on a single-row path), and a rank order term or a headline without exactly one usable full-text filter on its column |
| `invalid_parameter` | a reserved query parameter or a path key: malformed query-string escape, a duplicate parameter, `limit`/`offset`/`count` values, `order=` grammar (direction and nulls tokens, related-order paths, JSON paths, `.st_distance` on a non-spatial column, related ordering while it is disabled), `distinct=` grammar and column types, `on_conflict=` misuse, a list parameter on a single-row path or a write (`limit`, `order`, …), a per-embed parameter embeds do not take (`posts.after=`), `~geometry=` misuse; the reference parameters `~near=`, `~trgm=`, `~vec=`, `~latlon=` and `~levenshtein=`: missing where an order term, score column or threshold needs one, malformed, or present with nothing that uses it; a path key value that does not coerce to its column's type (`/users/abc`) or has the wrong number of parts. Also the code of a hook's `UserError` that names none |
| `invalid_select` | `select=` grammar: unbalanced parentheses, empty item, bad alias, a cast or JSON path where none applies, hints on a non-embed, arguments on a column that takes none, an explicit select of a raster column (never readable), bad spatial (`transform=`, `precision=`, …), raster (`band=`), headline or derived-column arguments; aggregates ([aggregates.md](aggregates.md#errors)): a cast outside an aggregate or to a type outside the allowlist (`::regclass`), an aggregate without the input type (`text.sum()`), an embed or ungroupable column as a grouping key, duplicate response keys, aggregates on single-row paths, mutations or in to-one embeds, `distinct=` with aggregates |
| `invalid_argument` | a function or computed-column argument value: an RPC argument (query parameter on `GET`, JSON member on `POST`) or a `select=key(k=v)` argument that does not coerce to the argument's type, and a row-typed argument that is not a JSON object (array of objects) of the resource's readable columns |
| `invalid_body` | request body: not an object/array as required, unreadable or trailing data, unknown key, empty patch, missing required columns, missing conflict-target columns, both `create` and `connect`, malformed nested operation, more than `max_operations` nested operations, uncoercible value, empty `[]` bulk, single-row `PUT` body key ≠ path key (PostgREST `PGRST115`), an RPC body that is not a JSON object |
| `invalid_cursor` | `after`/`before` cursor undecodable or minted for a different resource |
| `invalid_range` | `Range` header with the last item before the first, or no overlap with `offset`/`limit` ([pagination.md](pagination.md#range-request-header)) |
| `invalid_preference` | `Prefer: handling=strict` with an unknown token or a malformed value (PostgREST `PGRST122`); `resolution=merge-duplicates` on a resource without `PUT`; `tx=` while `transactions.end` has no override; `partial=` while `transactions.partial.enabled` is off or with a value other than `items`; an unknown `timezone=` |
| `unknown_column` | column, computed or pseudo-column name not on the resource, in `select=`, filters, `order=`, `distinct=`, `on_conflict=`; any name starting with `~` that is not one of the resource's pseudo-columns |
| `unknown_relation` | relation key in `select=`, a filter/order path, a per-embed param, or a nested-mutation body that matches no FK of the resource; a `!hint` that selects none of the key's FKs (`posts!nope(id)`; the detail lists the hints that would); a nested-mutation operation the relation does not offer (privilege, `@omit`, FK column not writable, referenced column hidden, `NOT NULL` FK for `disconnect`) |
| `ambiguous_relation` | relation key matches ≥ 2 FKs and carries no `!hint`; the detail lists the candidates |
| `unknown_argument` | RPC argument, computed-column argument or headline option name not declared |
| `missing_argument` | RPC or computed-column call omits an argument that has no `DEFAULT` (or, on a variadic function, one before a passed argument); the detail lists them |
| `column_not_filterable` | column exists but is outside the filter policy (`filters.indexed_only`, `@omit filter`); a pseudo-column that takes no filters (every one but the integer contrib accessors) |
| `column_not_orderable` | column exists but is outside the order policy; a pseudo-column that takes no order terms; with aggregates, an `order=` term that is not a grouping column |
| `column_readonly` | generated or identity-`ALWAYS` column in a write body |
| `keyset_unsupported` | `after`/`before` on a resource without a PK, `before` combined with `offset`, any cursor combined with a `Range` header, `distinct=` or aggregate functions with keyset |
| `aggregates_disabled` | an aggregate function in `select=` while `aggregates.enabled` is off (PostgREST `PGRST123`) |
| `temporal_conflict_target` | `PUT` or `POST` with `Prefer: resolution=merge-duplicates` whose conflict target is a temporal (`WITHOUT OVERLAPS`, PostgreSQL 18) key — `?on_conflict=` naming one, or the default target on a table whose primary key is temporal (`ignore-duplicates` accepts them); `ON CONFLICT` cannot infer the exclusion constraint behind it (PostgreSQL would fail with `42P10`) |
| `unfiltered_mutation` | bulk `PATCH`/`DELETE` on a collection path without a root filter or `!inner` embed while `mutations.allow_unfiltered` is off |
| `if_match_unsupported` | `If-Match` on any request while `server.etags` is off; with it on, `If-Match` on a write that cannot honour it (bulk `PATCH`/`DELETE` on a collection path, `PUT` on a single-row path, a `PATCH` carrying nested-mutation keys) |
| `max_affected_exceeded` | `Prefer: handling=strict, max-affected=N` and the write touched more than `N` rows; rolled back (PostgREST `PGRST124`) |

### Limits

| `code` | Status | Meaning |
|---|---|---|
| `page_size_exceeded` | 400 | `limit` > `limits.max_page_size` (also on embeds and RPC lists) |
| `cost_exceeded` | 400 | estimated cost > `limits.max_cost` |
| `depth_exceeded` | 400 | `select=` nesting deeper than `limits.max_depth`; nested-mutation depth > `max_depth` |
| `body_too_large` | 413 | body > `limits.max_body_bytes` |

### Identity

| `code` | Status | Meaning |
|---|---|---|
| `invalid_token` | 401 | JWT/headers present but invalid (signature, exp, iss/aud, JWKS miss or unavailable, malformed, a role claim that is not a string; a claim header misspelt, repeated, folding onto another claim, outside `auth.headers.claims` or sent by a peer outside `auth.headers.trusted_proxies`); `WWW-Authenticate: Bearer error="invalid_token"` |
| `auth_required` | 401 | no credentials and `roles.anonymous` empty (the default in `jwt` and `headers` mode); `WWW-Authenticate: Bearer` |
| `role_not_allowed` | 403 | credential valid, but the resolved role is not in `roles.allowed`, or is `none`, `public`, a `pg_*` role or not `[A-Za-z0-9_]+`, or the request resolves to no role at all; `WWW-Authenticate: Bearer error="insufficient_scope"` |

### Routing, methods and representations

| `code` | Status | Meaning |
|---|---|---|
| `not_found` | 404 | no such route/resource/function, or single-row path matched zero rows (incl. rows hidden by RLS), or a nested `connect` / `update` / `delete` matched no row (detail `nested "<path>": matched 0 rows`; the whole statement is rolled back) |
| `method_not_allowed` | 405 | method not registered on the path (view, missing privilege, `@omit`, volatile GET); carries `Allow` |
| `read_only` | 405 | write attempted while `server.read_only` (config or `App.SetReadOnly`); carries `Allow` |
| `not_acceptable` | 406 | `Accept` unsatisfiable (incl. `text/csv` with embeds or on non-list responses, `application/geo+json` without a spatial column in the selection) |
| `etag_mismatch` | 412 | `If-Match` did not match the current row hash |
| `unsupported_media_type` | 415 | request body `Content-Type` is not `application/json` |
| `range_not_satisfiable` | 416 | counted list whose `offset` / `Range` start is past the total; carries `Content-Range: */<total>` (PostgREST `PGRST103`) |

### Server and database

| `code` | Status | Meaning |
|---|---|---|
| `internal` | 500 | anything else pdbr-side (hook error, unexpected compile failure, panic) |
| `unavailable` | 503 | no database connection (pool acquire failed, connection refused/reset/dropped, `net.Error`, unexpected EOF) with no SQLSTATE; `detail` "database unavailable" (full error in `dev`); carries `Retry-After` |
| `timeout` | 504 | `server.request_timeout` elapsed (context deadline) |
| `pg:<SQLSTATE>` | per [Status mapping](#status-mapping) | database error; the five-character SQLSTATE verbatim (`pg:23505`, `pg:42501`, `pg:40001`) |

## Status mapping

Everything pdbr renders passes through one mapping (grammar/plan errors are
typed Go errors; database errors go through `pdbcore/pgexec.Classify`, which
returns `{Class, SQLState, Message(detail)}`):

| Condition | Status | `code` |
|---|---|---|
| URL grammar, query param, body validation errors, unfiltered bulk write, invalid `Prefer` under `handling=strict`, `max-affected` exceeded | 400 | pdbr code |
| `21000` cardinality violation (`ON CONFLICT DO UPDATE command cannot affect row a second time`: a bulk upsert naming one conflict key twice; a scalar subquery returning several rows), message passed through in prod | 400 | `pg:21000` |
| `22*` data exception (`22P02` invalid text representation, `22001` value too long, `22003` out of range, …) | 400 | `pg:22…` |
| `23502` not-null violation | 400 | `pg:23502` |
| `23514` check violation | 400 | `pg:23514` |
| `42*` other than `42501` (undefined function/column, syntax — should not happen; message "invalid operation" in prod/strict) | 400 | `pg:42…` |
| malformed full-text / ltree operand (`pdbcore/pgexec` contrib input errors): `42601` raised while parsing a `tsquery`/`tsvector`/`ltree`/`lquery`/`ltxtquery` value (`syntax error in tsquery: …`, `ltree syntax error …`, …), `42704` unknown text search configuration | 400 | `pg:42601` / `pg:42704` |
| PostGIS input error (`XX000` with `Operation on mixed SRID geometries`, `parse error - invalid geometry` or an `lwgeom_*` message): an operand or body value PostGIS rejects | 400 | `pg:XX000` |
| raster / geometry SRID mismatch (`P0001` with `Raster and geometry do not have the same SRID`, raised by PostGIS when a raster `st_intersects` operand in another SRID overlaps a row's raster) | 400 | `pg:P0001` |
| no credentials and no anonymous role | 401 | `auth_required` |
| invalid credentials | 401 | `invalid_token` |
| `42501` insufficient privilege (incl. RLS `RETURNING` filtering) | 403 | `pg:42501` |
| resolved role not in `roles.allowed`, not assumable (`none`, `public`, `pg_*`), or no role at all | 403 | `role_not_allowed` |
| no route; single-row path or keyed mutation matched nothing | 404 | `not_found` |
| method not on resource / read-only / volatile via GET | 405 + `Allow` | `method_not_allowed` / `read_only` |
| `Accept` unsatisfiable | 406 | `not_acceptable` |
| `23505` unique violation, `23503` FK violation, `23001` restrict violation (PostgreSQL 18+ reports `ON DELETE/UPDATE RESTRICT` as `23001`, older servers as `23503`), `23P01` exclusion violation (also a temporal `WITHOUT OVERLAPS` key duplicate) | 409 | `pg:235…` / `pg:23001` / `pg:23P01` |
| `40001` serialization failure / `40P01` deadlock **after** `transactions.max_retries` re-runs | 409 | `pg:40001` / `pg:40P01` |
| `If-Match` mismatch | 412 | `etag_mismatch` |
| body larger than `limits.max_body_bytes` | 413 | `body_too_large` |
| body `Content-Type` not JSON | 415 | `unsupported_media_type` |
| any other PG SQLSTATE (`2D000`, `P0001` `RAISE EXCEPTION` other than the raster SRID mismatch above, `XX000` other than the PostGIS input errors above, …) | 500 | `pg:<SQLSTATE>` |
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
| `prod` (default) | `23*`, `22*` and `21*` messages pass through, and so do contrib input errors (`syntax error in tsquery: …`); PostGIS input errors → `invalid geometry`; `42501` → `permission denied`; other `42*` → `invalid operation`; everything else → `database error` | absent |
| `strict` | `23*` → `constraint violation`; `21*`, `22*`, contrib and PostGIS input errors → `invalid input value`; rest as `prod` | absent |

In `prod`, a `23*` message is replaced by a generic one (`not-null
violation`, `foreign key violation`, `unique violation`, `check
violation`, `exclusion violation`, else `constraint violation`) when it
names something the serving API hides: a table missing from the catalog
(bare `@omit`, a [schema contract](contracts.md), a schema outside
`schema.schemas`), a column hidden from both responses and bodies, a
foreign key the API dropped or whose referenced table is hidden, or a
primary key, unique, check or exclusion constraint or unique index that
references a hidden column — in its key, an index expression or a
partial-index predicate, or anywhere in a check's expression (a check's
default name `<table>_<column>_check` spells the column). A constraint the
catalog does not record (a partition's own copy of an index, a constraint
added after the schema was loaded) counts as hidden when the table has
any hidden column. A `NOT NULL` column hidden that way is
not listed by the `missing required column(s)` check either; the
database's not-null violation answers instead.

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
 "instance":"/users","code":"pg:23505",
 "pg":{"sqlstate":"23505","message":"duplicate key value violates unique constraint \"users_email_key\"",
       "detail":"Key (email)=(a@b.c) already exists.","table":"users","constraint":"users_email_key"}}
```

```json
// errors.detail: prod
{"type":"about:blank","title":"Conflict","status":409,
 "detail":"duplicate key value violates unique constraint \"users_email_key\"",
 "instance":"/users","code":"pg:23505"}
```

```json
// errors.detail: strict
{"type":"about:blank","title":"Conflict","status":409,
 "detail":"constraint violation",
 "instance":"/users","code":"pg:23505"}
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
| `/{table}/{pk}` (identity `ALWAYS` / generated key) | `GET, HEAD, PATCH, DELETE` |
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
  `select=`, or with `server.csv: false` → `406`. `application/geo+json` is
  supported on `GET`/`HEAD` reads of table rows over a spatial column;
  without one in `select=`, with aggregates or on a mutation → `406`
  ([PostGIS](postgis.md#accept-applicationgeojson)). Error bodies are always
  `application/problem+json` even when the client only accepted `text/csv`.

## `412`

`If-Match` on `PATCH`/`DELETE` single-row paths ([mutations.md](mutations.md#if-match-on-patch--delete)):
row exists and its row hash (the `<row>` part of the ETag) differs →
`412 etag_mismatch`, transaction rolled back, no body change. Row missing →
`404` (takes precedence over `412`). `If-Match` on bulk `PATCH`/`DELETE` and
on a single-row `PUT` is `400 if_match_unsupported`; on other routes it is
ignored. With `server.etags: false`, `If-Match` is `400
if_match_unsupported` on every route instead of being ignored.
`If-None-Match` matching a single-row `GET`'s whole ETag → `304` (no body,
not a problem document).

## `413`

Request bodies are read through a `MaxBytesReader` at
`limits.max_body_bytes` (1 MiB); overflow → `413 body_too_large` before JSON
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
  the status (default `400`) and code the error carries, with the message
  as `detail`; a `UserError` without a code is `invalid_parameter`. Use a
  code from the vocabulary above, which is closed.
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
