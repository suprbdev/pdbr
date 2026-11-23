# HTTP API reference

pdbr introspects `pg_catalog` and serves a PostgREST-style REST API. Every
request — however many embeds, filters or nested writes it carries — compiles
to **one** parameterized SQL statement whose single `json` column is the
response body; Go only maps status codes and headers.

This document is the URL and header reference. Filters live in
[filtering.md](filtering.md); paging, ordering and cursors in
[pagination.md](pagination.md).

## Base path and routes

Everything except the health endpoints is mounted under `server.base_path`
(default `/api`; `""` mounts at the root). `{base}` below is that prefix.

| Method / route | Meaning | Status |
|---|---|---|
| `GET {base}/{table}` | list rows → JSON array | `200` |
| `HEAD {base}/{table}` | list headers only (incl. `X-Total-Count` with `count=true`), no body | `200` |
| `GET {base}/{table}/{pk}` | one row by primary key → object; composite PK values comma-separated in PK column order, each percent-encoded (`,` in a value → `%2C`) | `200` / `404` |
| `GET {base}/{table}/by/{cols}/{vals}` | one row by unique constraint; `cols` = comma list equal (order-insensitive) to a unique constraint's column set, `vals` positional to `cols` | `200` / `404` |
| `POST {base}/{table}` | insert object → row; insert array → bulk (single `INSERT … VALUES (…),(…)`, columns = union, missing → `DEFAULT`); `{}` → `DEFAULT VALUES` | `201` |
| `PUT {base}/{table}` | upsert on the PK, or on `?on_conflict=col1,col2` (a unique constraint); conflict columns must be in the body | `201` inserted (`xmax = 0`) / `200` updated; arrays always `200` |
| `PUT {base}/{table}/{pk}`, `…/by/{cols}/{vals}` | upsert of the row the path names (key from the path; body key columns optional, must match); only where every key column is writable | `201` inserted / `200` updated |
| `PATCH {base}/{table}/{pk}`, `…/by/{cols}/{vals}` | partial update → row | `200` / `404` / `412` |
| `DELETE {base}/{table}/{pk}`, `…/by/{cols}/{vals}` | delete → deleted row | `200` (`204` with `Prefer: return=minimal`) / `404` / `412` |
| `PATCH`, `DELETE {base}/{table}?<filters>` | bulk write of every row the filters select → array of written rows; no filter → `400 unfiltered_mutation` unless `server.allow_unfiltered_mutations` ([mutations.md](mutations.md#bulk-patch--delete-by-filter)) | `200` (`204` with `Prefer: return=minimal`) |
| `GET {base}/{schema}/{table}…` | schema-qualified form of every route above | |
| `GET {base}/rpc/{fn}?arg=v` | stable/immutable function | see [RPC](#rpc-functions) |
| `POST {base}/rpc/{fn}` (JSON body `{arg: v}`) | any function; volatile via `GET` → `405` (`Allow: POST`) | |
| `GET {base}/openapi.json` | OpenAPI 3.1 document (`server.openapi`, default true) | `200` |
| `OPTIONS *` | CORS preflight when `server.cors_origins` is set | |
| `GET /healthz`, `GET /readyz` | liveness `{"status":"ok"}` — **not** under `{base}` | `200` |

`HEAD` is accepted wherever `GET` is and returns the same headers without a
body.

Views, materialized views and tables the connection role cannot write have no
write methods; calling one returns `405` with `Allow` listing what exists.
`server.read_only: true` turns every write into `405` (`code: read_only`) and
can be toggled at runtime through `App.SetReadOnly`.

### What gets generated

For each table/view in `schema.schemas` the connection role can `SELECT`:

| Postgres | pdbr |
|---|---|
| table `users` | resource `/users` (`/public/users` canonical; bare alias for `schema.schemas[0]`) |
| rows | `GET /users` (`select`, filters, `order`, `limit`/`offset`, `after`/`before`, `count`, `distinct`; aggregates `select=mood,count()` with `server.aggregates`, [aggregates.md](aggregates.md)) |
| primary key | `GET /users/{id}`, `PATCH`/`DELETE /users/{id}`, keyset cursors, `ETag` |
| unique constraints | `GET /users/by/email/{email}` (+ `PATCH`/`DELETE`), `PUT /users?on_conflict=email` |
| foreign keys | embeds `posts?select=*,users(*)` (forward) and `users?select=*,posts(*)` (backward), relation filters and order |
| PostgreSQL 18 temporal keys (`WITHOUT OVERLAPS`), `PERIOD` and `NOT ENFORCED` foreign keys | see [Temporal keys](#temporal-keys-and-postgresql-18-foreign-keys): no single-row routes or conflict targets for temporal keys, `PERIOD` embeds are arrays |
| partitioned table `orders` | one resource like any table (reads, writes and embeds span every partition; inserts are routed by PostgreSQL); its partitions are **not** resources unless `schema.expose_partitions: true`, and then only with the keys and FKs declared on the partition itself (inherited ones are recorded on the parent) |
| `INSERT`/`UPDATE`/`DELETE` privileges | `POST /users`, `PATCH /users/{id}`, `DELETE /users/{id}`, `PUT /users` |
| enums | verbatim labels in bodies, responses and filters (`mood=eq.happy`) |
| composite types | nested JSON objects (`"address": {"street": …}`) on read and write; arrays too |
| stable/immutable functions | `GET`/`POST /rpc/{fn}` (`RETURNS TABLE` / `OUT` params → objects keyed by the result columns) |
| volatile functions | `POST /rpc/{fn}` (mutation semantics) |
| `fn(<table>)` stable functions | computed columns (`users_post_count` → `select=post_count`) |
| PostGIS columns | GeoJSON in/out, `<col>_area`, `<col>_centroid`, … pseudo-columns, spatial filters, `.dist` ordering |
| everything | `GET /openapi.json` |

## Naming

Identifiers are raw PostgreSQL names — nothing is camel-cased, singularized or
prefixed:

- Path segment = table/view name verbatim (percent-encoded if needed). JSON
  keys = column names verbatim. Enum values = labels verbatim. RPC path =
  function name.
- **Multi-schema.** Every resource has a canonical `/{schema}/{table}` and
  `/{schema}/rpc/{fn}`. Resources in `schema.schemas[0]` also get the bare
  alias `/{table}` and `/rpc/{fn}`. A bare alias is suppressed with a startup
  warning when it collides with a reserved segment: `rpc`, `openapi.json`,
  `healthz`, `readyz`, `by`.
- **Relation keys** (embeds, filter paths, order paths, nested-mutation
  bodies): forward FK → the *referenced* table name; backward FK → the
  *referencing* table name. Two FKs to the same table, or a self-reference,
  yield the same key and require a hint: `rel!fk_constraint_name(...)` or
  `rel!column(...)`; unhinted → `400 ambiguous_relation` listing the
  candidates. `@fieldName` / `@foreignFieldName` on the FK comment set explicit
  keys and remove the need for a hint.
- **Computed columns**: function name with the `<table>_` prefix stripped
  (`users_post_count` → `post_count`); `@name` overrides. A real column with the
  same name wins and the computed column is skipped with a warning.
- **PostGIS pseudo-columns**: `<col>_area`, `_length`, `_perimeter`,
  `_centroid`, `_envelope`, `_srid`, `_geometry_type`, `_is_valid`, `_x`,
  `_y`, `_z`, `_m`, `_num_points`, `_num_geometries`, `_start_point`,
  `_end_point`, `_point_on_surface`, `_wkt`, `_ewkt`, `_is_empty`,
  `_is_simple`, `_xmin`, `_ymin`, `_xmax`, `_ymax` (the type-specific ones only
  where the column's typmod allows them — [postgis.md](postgis.md); a real
  column of that name wins).
- Overloaded functions and duplicate route names: keep-first + warning.

Route precedence between `/{schema}/{table}` and `/{table}/{pk}` (decided
in M1, as built in `api.Built.Match`): a first segment equal to a configured
schema name is always the schema-qualified form; a `schema.schemas[0]` table
whose name equals another configured schema loses its bare alias (startup
warning), like the reserved segments. `/{schema}/rpc/{fn}` wins over a table
named `rpc` in that schema, falling back to the table when no such function
exists.

## Query parameters

Reserved: `select`, `order`, `limit`, `offset`, `after`, `before`, `count`,
`distinct`, `on_conflict`; `near` and `trgm` only where no column, computed
column or RPC argument carries the name (there it is a filter or the
argument, and `.dist` / `.trgm_*` ordering is unavailable). Every other parameter is a filter
(`col=op.value`, `rel.col=op.value`, `and=`/`or=`/`not.and=`/`not.or=`) — see
[filtering.md](filtering.md). A column literally named like a reserved
parameter cannot be filtered in the bare form; use it inside `and=(…)`.

## `select=`

Absent `select` means all columns (`*`). Grammar:

```
select    := item ("," item)*
item      := field | alias ":" field | embed | "*" | [ alias ":" ] aggregate
field     := name [ "(" k "=" v ("," k "=" v)* ")" ]      -- args only for spatial projection (transform=<srid>, simplify=<tol>)
embed     := relkey [ "!" hint ]* "(" select ")"           -- hint := fk_name | column | "inner"
name      := column | computed column | postgis pseudo-column
aggregate := "count()" | column [ "::" type ] "." fn "()" [ "::" type ]   -- server.aggregates; type: allowlisted built-in or enum
fn        := count | sum | avg | max | min | extent | union | collect | centroid
```

Aggregate functions (off by default, `server.aggregates`) group the result
by the other items of the same `select=` (columns and computed fields, not
embeds): `select=author_id,count()`, `select=total:amount::numeric.sum()::text`,
`select=id,posts(count())`. See [aggregates.md](aggregates.md) for grouping,
embeds, the cast allowlist, ordering, paging, the
spatial `extent`/`union`/`collect`/`centroid` extensions and the differences
from PostgREST.

### Sparse fields and aliases

```http
GET /api/users?select=id,email
GET /api/users?select=id,n:email            -- alias: {"id": 1, "n": "a@b.c"}
GET /api/users?select=*,post_count          -- all columns + a computed column
```

Unknown names → `400 unknown_column`.

`*` expands to real columns only (PostgREST behaviour, as built in
`compile.rowJSON`); computed columns and PostGIS pseudo-columns must be named
explicitly (`select=*,post_count`). The OpenAPI resource schema still lists
them (`readOnly`, "Select explicitly: select=<key>").

### Embeds

An embed compiles to a `LEFT JOIN LATERAL` inside the same statement:

| Direction | Key | Result |
|---|---|---|
| forward (the FK is on this table) | referenced table name | object, or `null` when the FK is null, the referenced row is missing (`NOT ENFORCED` FK) or an embed filter excludes it; an array for a `PERIOD` FK |
| backward (the FK points at this table) | referencing table name | array (possibly empty) |

```http
GET /api/posts?select=id,title,users(id,email)          -- forward: post → author
GET /api/posts?select=id,title,author:users(email)      -- aliased
GET /api/users?select=id,posts(id,title,comments(id))   -- backward, nested
```

```json
[{"id": 1, "posts": [{"id": 7, "title": "Hello", "comments": [{"id": 3}]}]}]
```

Nesting depth is bounded by `server.max_embed_depth` (default 5); deeper →
`400 embed_depth_exceeded`. Cost is bounded by `server.max_cost` — see
[pagination.md](pagination.md#cost-limit).

### Hints

`comments.parent_id` references `comments`, so both the forward and the
backward key are `comments`; `posts` and `places` both point at `users`, but
that is one FK per table and needs no hint.

```http
GET /api/comments?select=id,parent:comments!parent_id(id,body)             -- forward by column
GET /api/comments?select=id,replies:comments!comments_parent_id_fkey(id)   -- backward by FK name
GET /api/posts?select=title,users!inner(email)&users.email=eq.a@b.c        -- inner
```

- `!fk_constraint_name` / `!column` disambiguate. `!column` names a column of
  the FK on the referencing side.
- `!inner` turns the lateral join into an inner one: a filter on the embed
  restricts the parent rows (compiled as `EXISTS`), and parents with no match
  are dropped instead of carrying `null` / `[]`.

Hints can be combined: `comments!parent_id!inner(id)`.

### Per-embed parameters

Array embeds (backward, and forward over a `PERIOD` FK) accept their own
paging, ordering and filters, addressed by the relation key (or its alias
when one is given):

```http
GET /api/users?select=id,posts(id,title)&posts.order=title.desc&posts.limit=5&posts.offset=5
GET /api/users?select=id,posts(id,title)&posts.published=is.true
```

Without `!inner` a filter on a backward embed only narrows the embedded list;
a filter on a forward embed nulls out the object. With `!inner` both also
restrict the parent. Embedded filters and orders follow the embedded table's
own [indexed-only policy](filtering.md#the-indexed-only-policy).

As built: `<embed>.limit`/`.order`/`.offset` and `<embed>.<col>=<op>.<value>`
address a **selected** embed by its response key — the alias when one is
given (`recent:posts(id)&recent.limit=3`, PostgREST style), the relation
key otherwise — and nest (`posts.comments.limit=2`). Per-embed
`after`/`before`/`count`/`select`/`distinct` are rejected (`400
invalid_filter`): embedded lists are offset-paged.

### Temporal keys and PostgreSQL 18 foreign keys

PostgreSQL 18 adds temporal keys, `PRIMARY KEY` / `UNIQUE (…, valid_at
WITHOUT OVERLAPS)`: the last column is a range (or multirange), the leading
columns repeat once per non-overlapping period. It also adds temporal
foreign keys, `FOREIGN KEY (…, PERIOD valid_at) REFERENCES t (…, PERIOD
valid_at)`, and `NOT ENFORCED` foreign keys. pdbr handles them like this:

| Object | Behaviour |
|---|---|
| temporal primary key | no `/{pk}` row route (`404`), no `ETag`, not the default `PUT` conflict target; it still orders the default listing and backs keyset cursors (the range value travels in the cursor) |
| temporal unique constraint | no `/by/{cols}/{vals}` route |
| `?on_conflict=` naming a temporal key | `400 temporal_conflict_target` on `PUT` and `Prefer: resolution=merge-duplicates`: PostgreSQL enforces the key as an exclusion constraint, which `ON CONFLICT (cols)` cannot infer and `DO UPDATE` does not support; `resolution=ignore-duplicates` accepts it (`ON CONFLICT ON CONSTRAINT … DO NOTHING`) |
| `PUT` | offered only when the table has a plain primary key or unique constraint; a table whose only key is temporal has no `PUT` (`405`) and, without a row route, is updated and deleted only by filter on the collection (`PATCH`/`DELETE /bookings?room_id=eq.1&…`) |
| duplicate (overlapping) period on insert/update | `409 pg:23P01` (exclusion violation) |
| `PERIOD` FK, forward embed | an **array** of every referenced row whose period overlaps the row's own (the child's period may be covered by several parent rows), ordered by the referenced primary key; per-embed parameters apply |
| `PERIOD` FK, backward embed | an array of every referencing row whose period overlaps |
| `PERIOD` FK, joins | `child.k = parent.k AND child.valid_at && parent.valid_at` for embeds, `!inner`, relation filters (`!none`/`!every` accepted in both directions) and related order terms (aggregated with `MIN`/`MAX` in both directions) |
| `PERIOD` FK violation | `409 pg:23503` |
| `NOT ENFORCED` FK | an ordinary relation; the forward embed is `null` when no referenced row exists, even for `NOT NULL` FK columns (the OpenAPI property is nullable and says so) |

```http
GET /api/booking_notes?select=id,note,room_bookings(guest,valid_at)
```

```json
[{"id": 1, "note": "late checkout", "room_bookings": [
  {"guest": "ada", "valid_at": "[2026-01-01,2026-01-05)"},
  {"guest": "bo", "valid_at": "[2026-01-05,2026-01-10)"}]}]
```

Nested writes over a `PERIOD` FK copy only the equality columns: see
[mutations.md](mutations.md#temporal-period-relations).

### Computed columns and PostGIS

```http
GET /api/users?select=id,post_count                       -- users_post_count(users) → "post_count"
GET /api/places?select=name,location,location_srid,area_area
GET /api/places?select=name,web:location(transform=3857),coarse:area(simplify=0.01)
```

Computed columns come from stable/immutable functions whose first argument is
the table's row type; set-returning ones become arrays. Extra arguments are
passed as `field(k=v,…)`: `select=id,excerpt(max_chars=80)`, aliased
`short:excerpt(max_chars=40)`. Values are bare text or double-quoted
(`name="a,b"`), coerced by the argument's type and bound as parameters; an
omitted argument takes its `DEFAULT`, an omitted argument without one → `400
missing_argument`, an unknown name → `400 unknown_argument`, a bad value →
`400 invalid_filter`. A `SETOF <table>` column takes its nested select after
the arguments: `recent_posts(n=2,id,title)`. Parenthesised arguments on
spatial columns and their pseudo-columns are `transform=<srid>` →
`ST_Transform`, `simplify=<tolerance>` → `ST_SimplifyPreserveTopology`
(`postgis` plugin). Spatial values are GeoJSON
on output; GeoJSON or WKT/EWKT on input.

## RPC (functions)

```http
GET  /api/rpc/search_posts?q=hello
POST /api/rpc/search_posts            {"q": "hello"}
```

- `GET` for stable/immutable functions, `POST` for any volatility. Volatile
  functions run with mutation semantics (transaction, `405` when read-only).
- Arguments by name, coerced by pg type; missing → SQL `NULL`; unknown →
  `400`. Enum labels verbatim; spatial arguments GeoJSON or WKT.
- Returns: scalar → JSON value `200`; `void` → `204`; single row (table or
  composite) → object `200` (`null` when the function returns NULL); `SETOF
  <table>` → array with full `select=` (embeds included), filters, `order=`,
  `limit`/`offset`, `count=true` and CSV — offset paging only, no cursors;
  `SETOF <scalar>` → array of values. A function returning the
  `rls.auth.jwt_type` composite returns a minted HS256 JWT string.
- Not exposed: functions without EXECUTE privilege, extension-owned, unnamed
  args, `OUT`/`INOUT`/`VARIADIC`, trigger/internal returns. Overloads →
  keep-first + warning. `schema.functions: false` removes RPC and computed
  columns entirely.

## Request and response bodies

- Responses are `application/json; charset=utf-8`. Request bodies must be
  `application/json` → otherwise `415 unsupported_media_type`.
- `int8`, `numeric` and `money` are serialized as JSON **strings** (no
  precision loss); on input a JSON number or string is accepted.
- `bytea` is base64. Timestamps are ISO 8601. Composite-typed columns are
  nested objects (arrays of composites too). Enum labels verbatim.
- Objects are built with `json_build_object` / `json_agg` (as PostgREST
  does), so keys follow the `select=` order (embeds after the columns,
  composite attributes in declaration order), a key selected twice appears
  once with its last value, `json` columns come back as stored, `float8`
  uses PostgreSQL's float output (`1e+20`), and the text has PostgreSQL's spacing
  (`{"id" : 1}`): compare bodies as JSON, never byte for byte.
- Body validation: unknown key → `400 invalid_body`; generated/identity
  columns → `400 column_readonly`; on create, `NOT NULL` columns without a
  default are required; on `PATCH` everything is optional.
- `select=` applies to mutation responses (compiled against the `__mut` CTE,
  embeds work; `RETURNING` lists only referenced columns).
- Nested writes on `POST` (plugin `nested-mutations`): forward key →
  `{"create": {…}}` or `{"connect": {<key cols>}}` (exactly one; the key is
  the referenced columns, the primary key or one unique constraint —
  `{"email": "…"}` connects a user); backward key → `{"create": [{…}, …]}`;
  depth ≤ 3 (`plugins.settings.nested-mutations.max_depth`); FK columns
  become optional when nested input is provided
  ([mutations.md](mutations.md#nested-mutations-plugin-nested-mutations)).

## Content negotiation

| Request | Response |
|---|---|
| no `Accept`, `*/*`, `application/*`, `application/json` | JSON |
| `Accept: text/csv` (or `text/*`) on a list `GET`/`HEAD` or set-returning RPC `GET`/`HEAD` | CSV, `Content-Type: text/csv; charset=utf-8` |
| `Accept: text/csv` with an embed in `select=`, on a single-row path, on a mutation, on a non-set RPC, or with `server.csv: false` | `406 not_acceptable` (the `detail` says which) |
| any other `Accept` | `406 not_acceptable` |

Media ranges are ranked by `q` then specificity, so `text/csv;q=0.5,
application/json` is JSON and `text/csv, */*;q=0.1` falls back to JSON when
CSV is unavailable.

CSV rules (RFC 4180, `internal/server/csv.go`):

- One header row, then one record per row, `CRLF` line ends; fields are
  quoted by the encoder when they contain `,`, `"`, line breaks or leading
  spaces (`"` doubled).
- Column order = `select=` order: `*` expands to the resource's columns in
  catalog order, explicit items follow (an unaliased duplicate of a `*` column
  is dropped), aliases rename the header — exactly the compiled projection.
  Set-returning composite functions use the type's attributes; scalar sets
  (`SETOF text`, `SETOF mood`) have a single `value` column.
- Values: strings verbatim, numbers/booleans as their JSON literals,
  arrays / json / composites / GeoJSON as compact JSON text, NULL and
  missing keys as an empty field (no `\N` sentinel).
- The SQL is identical to the JSON request (goldens unaffected); the server
  re-encodes the jsonb array on the way out. `X-Total-Count`, cursors and
  `Link` are unchanged, and `HEAD` returns the same headers with no body.
- Errors are always `application/problem+json`, even when only `text/csv`
  was acceptable.

CSV is toggled by `server.csv` (default true); when off the OpenAPI document
drops the `text/csv` content and the `Accept` header parameter.

## `Prefer`

| Header | Effect |
|---|---|
| `Prefer: return=representation` (default) | body = affected row(s) |
| `Prefer: return=minimal` / `return=headers-only` | `204` and no body; a single-object `POST` (or a `PUT` that inserted) returns `201` + `Location` and no body |
| `Prefer: handling=strict` | unknown or malformed tokens → `400 invalid_preference` (default `handling=lenient` ignores them) |
| `Prefer: handling=strict, max-affected=N` | `PATCH`/`DELETE` writing more than `N` rows → `400 max_affected_exceeded`, rolled back |
| `Prefer: resolution=merge-duplicates\|ignore-duplicates` | `POST` upserts / skips conflicting rows on `?on_conflict=` or the PK ([mutations.md](mutations.md#post-with-prefer-resolution)) |
| `Prefer: missing=default` | accepted on `POST`/`PUT`; omitted keys always take their default in pdbr |
| `Prefer: count=exact\|planned\|estimated` | list `GET`/`HEAD`: total in `Content-Range` and `X-Total-Count` ([pagination.md](pagination.md#prefer-count-and-content-range)); bulk `PATCH`/`DELETE`: exact written-row total in `Content-Range` |

Honoured tokens are echoed in `Preference-Applied`. Details and the
transaction semantics: [mutations.md](mutations.md#prefer).

## ETag / conditional requests

Single-row `GET`s carry a weak `ETag: W/"<hex>"` = `md5(to_jsonb(row)::text)`
over **all** columns of the resource, computed in SQL and independent of
`select=`. `If-None-Match` matching it → `304`.

`PATCH`/`DELETE` on single-row paths honour `If-Match`. The check is part of
the one statement:

```sql
WITH cur AS (SELECT md5(to_jsonb(t)::text) AS h FROM users t WHERE id = $1),
     __mut AS (UPDATE users SET … WHERE id = $1 AND (SELECT h FROM cur) = $2 RETURNING …)
SELECT …
```

No `cur` row → `404`; hash mismatch → `412 etag_mismatch`. Atomic without a
second round-trip. `server.etags` (default true) toggles the whole feature.

**Caveat — column-level grants.** Hashing the whole row needs `SELECT` on every
column. If a role has column-level grants on a table, single-row `GET`s on it
fail with `403`; disable `server.etags` for such deployments.

List responses carry no `ETag`.

## Response headers

| Header | When | Meaning |
|---|---|---|
| `Content-Range` | offset-addressed list pages and bulk `PATCH`/`DELETE` | `<first>-<last>/<total>` (`*` when empty or without a count) |
| `X-Total-Count` | `count=true` or `Prefer: count=` on lists / `SETOF` RPC | total rows after filters (distinct-aware); computed by a `count(*)` subquery in the same statement |
| `X-Next-Cursor` | keyset-capable list, next page exists | opaque cursor for `after=` |
| `X-Prev-Cursor` | keyset-capable list, previous page exists | opaque cursor for `before=` |
| `Link` | lists | `<url?after=…>; rel="next", <url?before=…>; rel="prev"` built from the request URL (relative: `{base}{path}?{query}`, other params kept verbatim and in place); offset-only resources get `offset=` links and no cursors |
| `ETag` | single-row `GET` | weak row hash |
| `Location` | `201` from a single-object `POST` (or inserting `PUT`) on a resource with a single-column PK | `{base}/{table}/{pk}` |
| `Preference-Applied` | a request whose `Prefer` tokens were honoured | the honoured tokens, comma-separated (`return=minimal, handling=strict`) |
| `Allow` | every `405` (API routes, `/openapi.json`, `/healthz`, `/readyz`) | methods that exist on the path; empty when none does (a volatile function's route under `server.read_only`) |
| `Content-Type` | always | `application/json; charset=utf-8`, `text/csv; charset=utf-8`, or `application/problem+json` |

Cursor and `Link` details: [pagination.md](pagination.md).

## Limits and errors

| Limit | Config | Over |
|---|---|---|
| page size | `server.max_page_size` (100) | `400 page_size_exceeded` |
| embed depth | `server.max_embed_depth` (5) | `400 embed_depth_exceeded` |
| request cost | `server.max_cost` (10000) | `400 cost_exceeded` |
| body size | `server.max_body_bytes` (1 MiB) | `413` |
| request time | `server.request_timeout` (30s) | `504` |
| statement time | `database.statement_timeout` (30s) | `504` |

Errors are RFC 9457 problem documents (`application/problem+json`) with a
stable `code` extension:

```json
{"type":"about:blank","title":"Conflict","status":409,
 "detail":"duplicate key value violates unique constraint \"users_email_key\"",
 "instance":"/api/users","code":"pg:23505"}
```

`errors.detail: dev` adds a `pg` object (`sqlstate`, `message`, `detail`,
`hint`, `table`, `constraint`); `prod` (default) passes through `22*`/`23*`
messages and makes the rest generic; `strict` makes every database message
generic (`code` stays).

## Worked examples

Fixture: `users(id, email, mood, tags, settings, address)`,
`posts(id, author_id, title, published)`,
`comments(id, post_id, parent_id, body)`, `places(id, name, location, area)`,
functions `search_posts(q)` and `users_post_count(users)`.

List with sparse fields, a filter and an embed:

```console
$ curl -s 'localhost:8080/api/users?select=id,email,posts(id,title)&mood=eq.happy&limit=2'
```
```json
[{"id":1,"email":"a@b.c","posts":[{"id":7,"title":"Hello"}]},
 {"id":4,"email":"d@e.f","posts":[]}]
```

Total count and cursors:

```console
$ curl -si 'localhost:8080/api/posts?order=title.asc&limit=2&count=true'
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8
X-Total-Count: 41
X-Next-Cursor: WyJwdWJsaWMucG9zdHMiLDJd
Link: </api/posts?order=title.asc&limit=2&count=true&after=WyJwdWJsaWMucG9zdHMiLDJd>; rel="next"

[{"id":7,"title":"Hello","author_id":1,"published":true},
 {"id":2,"title":"Intro","author_id":1,"published":false}]
```

Single row by PK with ETag, then a conditional re-fetch:

```console
$ curl -si 'localhost:8080/api/users/1?select=id,email,address'
HTTP/1.1 200 OK
ETag: W/"9e107d9d372bb6826bd81d3542a419d6"

{"id":1,"email":"a@b.c","address":{"street":"1 Main St","city":"Leeds","mood":"ok"}}

$ curl -si -H 'If-None-Match: W/"9e107d9d372bb6826bd81d3542a419d6"' 'localhost:8080/api/users/1'
HTTP/1.1 304 Not Modified
```

Lookup by unique constraint, with a computed column:

```console
$ curl -s 'localhost:8080/api/users/by/email/a%40b.c?select=id,post_count'
{"id":1,"post_count":"3"}
```

Insert with a nested forward `connect`, minimal response:

```console
$ curl -si -X POST -H 'Content-Type: application/json' -H 'Prefer: return=minimal' \
    -d '{"title":"New","published":false,"users":{"connect":{"id":1}}}' \
    'localhost:8080/api/posts'
HTTP/1.1 201 Created
Location: /api/posts/42
```

Conditional update:

```console
$ curl -si -X PATCH -H 'Content-Type: application/json' -H 'If-Match: W/"stale"' \
    -d '{"published":true}' 'localhost:8080/api/posts/42'
HTTP/1.1 412 Precondition Failed
Content-Type: application/problem+json

{"type":"about:blank","title":"Precondition Failed","status":412,
 "detail":"row has changed since it was read","instance":"/api/posts/42","code":"etag_mismatch"}
```

Set-returning RPC as CSV, filtered and ordered:

```console
$ curl -s -H 'Accept: text/csv' 'localhost:8080/api/rpc/search_posts?q=hello&published=is.true&order=id.desc&limit=2'
id,author_id,title,published
9,2,Hello again,true
7,1,Hello,true
```

Nearest places to a point, with a pseudo-column:

```console
$ curl -s 'localhost:8080/api/places?select=name,location_srid&order=location.dist.asc&near=SRID%3D4326%3BPOINT(-0.1281%2051.508)&limit=1'
[{"name":"Trafalgar Square","location_srid":4326}]
```

Next: [filtering.md](filtering.md), [pagination.md](pagination.md).
