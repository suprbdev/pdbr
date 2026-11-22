# Mutations

As built (M3). Every write is one HTTP request → one parameterized SQL
statement → one jsonb `data` row. Go maps status and headers; PostgreSQL
builds the body. The SQL shown below is the golden output
(`internal/compile/testdata/{create,create_bulk,patch_pk,patch_ifmatch,
delete_pk,delete_minimal,upsert_on_conflict,upsert_bulk,mutation_select_embed,
composite_input,enum_input,bulk_patch,bulk_delete_embed,…}.sql`), long column
lists elided with `…`.

Routes (per table with the matching privilege; see [Method availability](#method-availability-405--allow)):

| Route | SQL | Success |
|---|---|---|
| `POST {base}/{table}` | `INSERT` (single or bulk) | `201` + row / array, `Location` for single |
| `PATCH {base}/{table}/{pk}` · `…/by/{cols}/{vals}` | `UPDATE … WHERE key` | `200` + row |
| `DELETE {base}/{table}/{pk}` · `…/by/{cols}/{vals}` | `DELETE … WHERE key` | `200` + row (`204` with `Prefer: return=minimal`) |
| `PUT {base}/{table}` | `INSERT … ON CONFLICT DO UPDATE` | `201` inserted / `200` updated; array → `200` |
| `PATCH {base}/{table}?<filters>` | `UPDATE … WHERE <filters>` | `200` + array of updated rows (`[]` when nothing matched) |
| `DELETE {base}/{table}?<filters>` | `DELETE … WHERE <filters>` | `200` + array of deleted rows (`204` with `Prefer: return=minimal`) |

Request bodies must be `application/json` (else `415`) and ≤ `server.max_body_bytes`
(1 MiB, else `413`). Responses are `application/json; charset=utf-8`; errors are
[Problem Details](errors.md).

Examples use the shared fixture (`users`, `posts`, `comments`; `pdbcore/testutil`).

## POST — create

Single object → `201`, the inserted row, and `Location` when the table has a
single-column primary key:

```http
POST /api/users
Content-Type: application/json

{"email": "a@b.c", "full_name": "Ada", "mood": "happy"}
```

```http
HTTP/1.1 201 Created
Location: /api/users/42
Content-Type: application/json; charset=utf-8

{"id":42,"email":"a@b.c","full_name":"Ada","mood":"happy","settings":null,"tags":null,"balance":"0","created_at":"2026-08-17T10:00:00+00:00","address":null,"prev_addresses":null,"moods":null}
```

```sql
WITH __mut AS (
  INSERT INTO "public"."users" ("email", "full_name", "mood")
  VALUES ($1, $2, $3)
  RETURNING "id", "email", "full_name", "mood", "settings", "tags", "balance", "created_at", "address", "prev_addresses", "moods"
)
SELECT jsonb_build_object('row', jsonb_build_object('id', t0."id", 'email', t0."email", 'full_name', t0."full_name", 'mood', t0."mood", …), 'key', jsonb_build_array(t0."id")) AS data
FROM __mut t0
-- args: ["a@b.c","Ada","happy"]
```

The `{'row', 'key'}` wrapper carries the primary key for `Location`
independently of `select=` (exec unwraps `row`; see [SQL shape
summary](#sql-shape-summary)).

- Body array → bulk insert: one `INSERT … VALUES (…),(…)`; the column list is the
  union of keys across elements; an element missing a column gets `DEFAULT` in
  its tuple. Response `201` + array (in `VALUES` order), no `Location`.
- `{}` → `INSERT INTO t DEFAULT VALUES` (all-default row; a table with a
  required column rejects it with `invalid_body` listing the column). `[]` →
  `400 invalid_body`. A bulk body of empty objects names the table's first
  column and inserts `DEFAULT` per tuple.
- Composite-PK tables get no `Location`. `Location` is `{base}/{table}/{pk}` for the
  bare alias when the table is in `schema.schemas[0]`, else `{base}/{schema}/{table}/{pk}`.
- `Prefer: return=minimal` → `201` with `Location` (single) and no body; the
  statement still runs `RETURNING <pk>` only.

```sql
-- POST /api/users  body: [{"email":"a@b.c","full_name":"Ada"},{"email":"d@e.f"}]
WITH __mut AS (
  INSERT INTO "public"."users" ("email", "full_name")
  VALUES ($1, $2), ($3, DEFAULT)
  RETURNING "id", "email", "full_name", …
)
SELECT coalesce(jsonb_agg(s2.data), '[]'::jsonb) AS data
FROM (
  SELECT jsonb_build_object('id', t0."id", 'email', t0."email", 'full_name', t0."full_name", …) AS data
  FROM __mut t0
) s2
-- args: ["a@b.c","Ada","d@e.f"]
```

Bulk `RETURNING` order follows `VALUES` order in practice but is not
guaranteed by PostgreSQL; v1 documents "insertion order" and adds no
`ORDER BY` (resolved in M3: keep it cheap; clients that need certainty
select the key and sort).

## PATCH — partial update

By primary key or by any unique constraint (same path grammar as [reads](api.md#base-path-and-routes):
composite PK values comma-separated in PK column order; `/by/{cols}/{vals}`
column set order-insensitive). Every body key is optional; an empty body
(`{}`) → `400 invalid_body`.

```http
PATCH /api/users/1
Content-Type: application/json

{"full_name": "Grace"}
```

```sql
WITH __mut AS (
  UPDATE "public"."users"
  SET "full_name" = $1
  WHERE "public"."users"."id" = $2
  RETURNING "id", "email", "full_name", …
)
SELECT jsonb_build_object('id', t0."id", …) AS data
FROM __mut t0
-- args: ["Grace",1]
```

```sql
-- PATCH /api/memberships/by/org_id,slug/7,eng
UPDATE "public"."memberships" SET … WHERE "public"."memberships"."org_id" = $2 AND "public"."memberships"."slug" = $3
```

| Outcome | Status |
|---|---|
| one row updated | `200` + row |
| no row matched (or RLS hid it) | `404 not_found` |
| `If-Match` present and stale | `412 etag_mismatch` ([below](#if-match-on-patch--delete)) |
| `Prefer: return=minimal` | `204`, `RETURNING 1` |

Updating a key column (PK or the unique columns used in the path) is allowed;
the row is matched by its pre-update key.

## DELETE

```http
DELETE /api/posts/2
```

```sql
WITH __mut AS (
  DELETE FROM "public"."posts"
  WHERE "public"."posts"."id" = $1
  RETURNING "id", "author_id", "title", "body", "published"
)
SELECT jsonb_build_object('id', t0."id", 'author_id', t0."author_id", 'title', t0."title", 'body', t0."body", 'published', t0."published") AS data
FROM __mut t0
-- args: [2]
```

`200` + the deleted row (`select=` applies, embeds resolve against the
pre-delete state visible to the statement's snapshot); `204` empty with
`Prefer: return=minimal` (`RETURNING 1`); `404` when nothing matched; `412` on
stale `If-Match`.

## Bulk PATCH / DELETE by filter

`PATCH` and `DELETE` on the collection path write every row the query
string's filters select. The filters are the read filters — same grammar,
operators, logic trees, relation and computed filters (advanced-filters) and
the same filter policy (`filters.indexed_only` → `400 column_not_filterable`)
— compiled by the same filter compiler against the DML target:

```http
PATCH /api/users?mood=eq.sad&id=gt.1
Content-Type: application/json

{"full_name": "Grace"}
```

```sql
WITH __mut AS (
  UPDATE "public"."users" m0
  SET "full_name" = $3
  WHERE m0."id" > $1 AND m0."mood" = $2
  RETURNING "id", "email", "full_name", …
)
SELECT jsonb_build_object('n', count(*), 'rows', coalesce(jsonb_agg(s3.data), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', t1."id", …) AS data
  FROM __mut t1
) s3
-- args: [1,"sad","Grace"]
```

| Rule | Behaviour |
|---|---|
| row set | exactly the rows the same `GET` would list: the root filters plus one `EXISTS` per `!inner` embed in `select=` (`DELETE /posts?select=id,users!inner(id)&users.email=eq.x` deletes only that author's posts) |
| no restriction | neither a root filter nor an `!inner` embed → `400 unfiltered_mutation` before any SQL runs, unless `server.allow_unfiltered_mutations: true` (then the DML has no `WHERE`; RLS still applies). The guard checks that a restriction is **present**, not how selective it is: a match-all filter (`id=gt.0`, `or=(id.is.null,id.not.is.null)`) passes, as with PostgREST's `pg-safeupdate`. Cap the blast radius with `Prefer: handling=strict, max-affected=N`. Per-embed filters on a plain embed (`users.email=…` with `users(...)` in `select=`) shape the response only and do not count — use `users!inner(...)`, or alias the embed (`author:users(email)`) so `users.email=` becomes a relation filter |
| body | `PATCH`: one JSON object (arrays → `400 invalid_body`), set on every matching row; `{}` → `400 invalid_body`. `DELETE` never reads a body |
| response | `200` + JSON array of the written rows (`select=` and embeds apply, compiled against `__mut`); `[]` when nothing matched — never `404`. `Prefer: return=minimal` / `headers-only` → `204` |
| `limit`, `offset`, `order`, `count`, `distinct`, `after`/`before`, `on_conflict` | `400 invalid_filter` (PostgREST's ordered "limited update/delete" is not supported) |
| `If-Match` | `400 if_match_unsupported` (with `server.etags` on; ETags address single rows) |
| `Prefer: max-affected=N` | with `handling=strict`: more than `N` rows → `400 max_affected_exceeded`, nothing written ([below](#prefer)) |
| availability | wherever the keyed `PATCH`/`DELETE` exists (UPDATE / DELETE privilege, no `@omit update|delete`) **and** the collection is listable: `@omit read` / `@omit all` remove bulk writes too (they would otherwise count hidden rows and, with `return=representation`, return them); PK-less tables included; views and read-only mode → `405` |

The DML target is aliased (`m0`) so relation filters correlate on it
(`EXISTS (SELECT 1 FROM "public"."users" r1 WHERE r1."id" = m0."author_id" …)`)
and the narrow-`RETURNING` scan (`t1."col"` / `__mut."col"`) never sees filter
columns. Golden files: `bulk_patch`, `bulk_patch_minimal`,
`bulk_patch_computed`, `bulk_delete_embed`, `bulk_delete_relation`,
`bulk_delete_inner`, `bulk_unfiltered`, `bulk_if_match`.

## `Prefer`

`Prefer` tokens are comma-separated, case-insensitive, may span several
headers, and the last occurrence of a token wins — `handling=` included
(`handling=strict, handling=lenient` is lenient). Every honoured token is
echoed in `Preference-Applied` (header order, e.g. `Preference-Applied:
return=minimal, handling=strict, max-affected=10`).

| Token | Applies to | Effect |
|---|---|---|
| `return=representation` | writes | default: the body is the written row(s) |
| `return=minimal` | writes | no body: `204`, or `201` + `Location` for a single-object create |
| `return=headers-only` | writes | same as `minimal` (pdbr's `minimal` already keeps `Location`) |
| `handling=lenient` | any request | default: unknown tokens and malformed values are ignored |
| `handling=strict` | any request | unknown tokens or malformed values → `400 invalid_preference` (PostgREST `PGRST122`); enables `max-affected` |
| `count=exact\|planned\|estimated` | bulk `PATCH`, `DELETE` | `Content-Range: 0-<n-1>/<n>` (the written-row count is always exact, so `Preference-Applied` says `count=exact`); lists: [pagination.md](pagination.md#prefer-count-and-content-range) |
| `max-affected=N` | `PATCH`, `DELETE` (bulk and keyed) | with `handling=strict` only (ignored and not echoed under lenient handling, as in PostgREST): writing more than `N` rows → `400 max_affected_exceeded`, nothing written (PostgREST `PGRST124`). A keyed write counts as one row |

`max-affected` keeps the single-statement invariant: the statement is
unchanged (the bulk shape already returns `n`, keyed shapes imply 0 or 1),
and exec compares the count inside the transaction right after the
statement and returns an error, which rolls the transaction back. A
request carrying the cap always runs in a transaction, even with
`transactions.mutations: false`. Raising from inside the statement was
rejected: SQL has no `RAISE` outside PL/pgSQL, and the usual tricks
(`1/0`, a failing cast in a `CASE`) surface as indistinguishable `22012` /
`22P02` user errors.

## PUT — upsert

`PUT {base}/{table}` with a full or partial row. Compiles to
`INSERT … ON CONFLICT (<target>) DO UPDATE SET c = EXCLUDED.c` for every
provided column that is not part of the conflict target.

```http
PUT /api/users?on_conflict=email
Content-Type: application/json

{"email": "a@b.c", "full_name": "Ada"}
```

```sql
WITH __mut AS (
  INSERT INTO "public"."users" ("email", "full_name")
  VALUES ($1, $2)
  ON CONFLICT ("email") DO UPDATE
  SET "full_name" = EXCLUDED."full_name"
  RETURNING "id", "email", "full_name", …, (xmax = 0) AS __inserted
)
SELECT jsonb_build_object('row', jsonb_build_object('id', t0."id", …), 'inserted', t0.__inserted, 'key', jsonb_build_array(t0."id")) AS data
FROM __mut t0
-- args: ["a@b.c","Ada"]
```

(The fixture's `users.id` is `GENERATED ALWAYS AS IDENTITY`, so the default
PK target is unusable there — the body cannot carry `id` — and
`?on_conflict=email` selects the unique constraint instead.)

Rules:

| Rule | Behaviour |
|---|---|
| default target | the primary key; a PK-less table needs `?on_conflict=` naming one of its unique constraints (else `400 invalid_body`); no PK and no unique constraint → `PUT` absent (`405`) |
| `?on_conflict=col1,col2` | column set (order-insensitive) must match a unique constraint or the PK → else `400 invalid_body` |
| conflict columns | must be present in the body (every element for arrays) → else `400 invalid_body` |
| non-key columns | every provided one becomes `SET c = EXCLUDED.c`; unprovided columns keep their existing values on conflict |
| only-key body | `SET k = EXCLUDED.k` for the key itself so the statement returns the existing row instead of zero rows |
| single object status | `201` when `xmax = 0` (fresh insert), `200` when the conflict branch ran |
| array body | one `INSERT … VALUES (…),(…) ON CONFLICT …`, always `200` + array; two elements with the same key in one request is a PG error (`21000`, → `500` sanitized) — same as PostgREST |
| `Location` | on `201` for single-column PK, as for POST |
| privileges | requires INSERT and UPDATE; either missing → `PUT` absent |
| `Prefer: return=minimal` | `201` + `Location` when inserted, `204` when updated; no body (`RETURNING (xmax = 0)`) |

`select=` applies as for POST. Generated / identity-`ALWAYS` columns cannot be
in the body (`column_readonly`), so a table whose PK is such a column can only
be upserted via `?on_conflict=` on another unique constraint (serial and
identity-`BY DEFAULT` keys are fine).

The `xmax` trick relies on `xmax` being 0 for a freshly inserted tuple in the
same transaction, which holds for `INSERT … ON CONFLICT` in every supported PG
version.

## `select=` on mutation responses

`select=` (same grammar as [reads](api.md#select), including embeds and aliases) shapes the
returned row. It compiles against the `__mut` CTE exactly like a table row
source, so embeds are `LEFT JOIN LATERAL` against the real tables correlated on
`__mut` columns:

```http
POST /api/posts?select=id,title,users(id,email)
```

```sql
WITH __mut AS (
  INSERT INTO "public"."posts" ("author_id", "title")
  VALUES ($1, $2)
  RETURNING "id", "author_id", "title"
)
SELECT jsonb_build_object('row', jsonb_build_object('id', t0."id", 'title', t0."title", 'users', l2.data), 'key', jsonb_build_array(t0."id")) AS data
FROM __mut t0
LEFT JOIN LATERAL (
  SELECT jsonb_build_object('id', t1."id", 'email', t1."email") AS data
  FROM "public"."users" t1
  WHERE t1."id" = t0."author_id"
  LIMIT 1
) l2 ON true
-- args: [1,"Hello"]
```

`RETURNING` lists exactly the columns the compiled response references
through `__mut` (here `id`, `title`, and `author_id` for the join
correlation) — never `RETURNING *`. This keeps mutations working for roles
holding column-level `SELECT` grants (see [RLS caveats](#rls-caveats-for-returning)).
Absent `select=` → all columns. Computed columns in `select=` force all columns
into `RETURNING` (they need the row type, see [rpc.md](rpc.md#computed-columns)).

Filters, `order=`, `limit`, `offset`, `count`, `distinct`, `after`/`before` are
rejected on mutation routes (`400`) — only `select=` (and `on_conflict` for
PUT) is meaningful; the exception is bulk `PATCH`/`DELETE` on the collection
path, where the filters select the rows. Per-embed params (`posts.order=`, `posts.limit=`) inside
`select=` embeds are honoured.

## Body validation

Validation happens in the parser (`internal/parse/body.go`) against the
catalog; failures are `400` before any SQL runs. Bulk element errors are
prefixed `element N:` and keep their code.

| Rule | Error |
|---|---|
| top-level must be an object (POST/PUT: object or non-empty array of objects) | `invalid_body` |
| key that is not a column of the resource (or a nested-mutation relation key) | `400 invalid_body` |
| nested-mutation `create` on a related resource without `POST` (no INSERT / `@omit create`) | `400 unknown_relation` |
| generated (`GENERATED ALWAYS AS … STORED`) or identity `ALWAYS` column | `400 column_readonly` |
| POST: `NOT NULL && !HasDefault` column missing | `400 invalid_body` listing the missing columns |
| PATCH: no required keys; empty patch | `400 invalid_body` |
| PUT: conflict-target columns missing | `400 invalid_body` |
| value not coercible to the column type (e.g. object for `int4`) | `400 invalid_body` |

Identity `BY DEFAULT` columns are meant to be writable (an explicit value
overrides the sequence), but pdbcore's catalog (`Column.Generated`) flags
both identity kinds alike, so as built every identity column is
`column_readonly` until pdbcore distinguishes them (tracked as an M3
leftover). Serial columns (`HasDefault`) are writable.

Unknown body keys use `invalid_body` (resolved in M3; `unknown_column` stays
reserved for query-side names).

Coercion (values are always `$n` parameters, cast on the PG side):

| PG type | Accepted JSON |
|---|---|
| `int2`/`int4`, `float4`/`float8`, `bool`, text-like | native JSON scalar |
| `int8`, `numeric`, `money` | JSON number **or** string (`"9007199254740993"`); returned as string |
| `uuid`, `date`, `time`, `timestamp[tz]`, `interval`, `bytea` (base64) | string, parsed by PG |
| enum | label verbatim (`"happy"`); unknown label → PG `22P02` → `400` |
| `json`/`jsonb` | any JSON value, passed as `$n::jsonb` |
| array | JSON array of the element mapping (`["a","b"]`, `[1,2]`); passed as a typed array parameter |
| composite | JSON object keyed by attribute name (`{"street":"…","city":"…"}`; unknown attributes → `invalid_body`) → `jsonb_populate_record(NULL::"public"."address", $n::jsonb)`; arrays of composites → one jsonb parameter expanded element-wise (`array_agg(jsonb_populate_record(NULL::t, NULLIF(e.v, 'null'::jsonb)) ORDER BY e.o) FROM jsonb_array_elements($n::jsonb) WITH ORDINALITY e(v, o)`, `[]` → empty array, `null` → NULL) |
| geometry/geography (postgis plugin) | GeoJSON object or WKT/EWKT string → `ST_GeomFromGeoJSON($n)` / `ST_GeomFromEWKT($n)` (`::geography` cast for geography columns; arrays → `ARRAY[…]::geometry[]`; `null` → `$n::geometry`) |
| `null` | SQL `NULL` (POST: only valid for nullable columns, else `23502` → `400`); the SQL text is the same as for a non-null value |

Unknown or unmapped PG types accept a JSON string (or number/bool, rendered
as text) passed as a bare `$n` typed by the target column.

## If-Match on PATCH / DELETE

Single-row GETs return `ETag: W/"<hex>"` = `md5(to_jsonb(row)::text)` over all
columns ([api.md](api.md#etag--conditional-requests), `server.etags`). `PATCH`/`DELETE` on single-row paths
honour `If-Match` in the same statement — no read-then-write race:

```http
PATCH /api/users/1
If-Match: W/"5d41402abc4b2a76b9719d911017c592"

{"full_name": "Grace"}
```

```sql
WITH cur AS (
  SELECT md5(to_jsonb(__cur)::text) AS h
  FROM "public"."users" __cur
  WHERE __cur."id" = $2
),
__mut AS (
  UPDATE "public"."users"
  SET "full_name" = $1
  WHERE "public"."users"."id" = $2
    AND (SELECT h FROM cur) = $3
  RETURNING "id", "email", "full_name", …
)
SELECT jsonb_build_object('found', EXISTS (SELECT 1 FROM cur), 'row', (
  SELECT jsonb_build_object('id', t0."id", …)
  FROM __mut t0
)) AS data
-- args: ["Grace",1,"5d41402abc4b2a76b9719d911017c592"]
```

| `found` | `row` | Response |
|---|---|---|
| `false` | `null` | `404 not_found` |
| `true` | `null` | `412 etag_mismatch` |
| `true` | object | `200` (or `204` with `return=minimal`) |

- Weak/strong prefix and quotes are stripped before comparison; `If-Match: *`
  means "must exist" (compiled without the hash predicate).
- Multiple ETags (`If-Match: W/"a", W/"b"`) → `= ANY($n)` with a `text[]`
  parameter.
- `server.etags: false` → `If-Match` is ignored (header not honoured, no `412`).
- `cur` hashes all columns, so `If-Match` needs `SELECT` on every column of the
  table (same caveat as `ETag` on reads).
- Without `If-Match` the plain single-CTE shape is used.

Mutation responses do not carry an `ETag` header in v1 (resolved in M3: a
second hash over the post-mutation row is cheap; revisit if clients want
PATCH → PATCH chains without an intermediate GET).

## SQL shape summary

Every mutation statement is `WITH __mut AS (<DML> RETURNING <cols>) SELECT
<jsonb> AS data`. The outer `SELECT` is the same row/list compiler used by
reads, with `__mut` as the row source; row JSON is `jsonb_build_object`
(chunked ≤ 40 pairs, `||`-concatenated), lists are `coalesce(jsonb_agg(…),
'[]'::jsonb)`.

When Go needs a value that is not part of the body (the primary key for
`Location`, upsert `inserted`, `If-Match` `found`, the affected count under
`return=minimal`), the statement wraps the body in one object and the server
unwraps `row`. As built there are four mutation shapes
(`compile.Statement.Shape`, unwrapped in `internal/exec/unwrap_mutation.go`):

| Shape | `data` | Used for |
|---|---|---|
| `mut_row` | the row JSON verbatim; zero rows → `404` | keyed `PATCH`/`DELETE` with `return=representation` and no `If-Match` |
| `mut_list` | the JSON array verbatim (`[]` when empty) | bulk `POST` (`201`) / bulk `PUT` (`200`) |
| `mut_wrap` | `{"row": obj\|null, "key": [pk], "inserted": bool, "found": bool, "n": int}` — members present per request | single `POST` (`row`,`key`), single `PUT` (`row`,`inserted`,`key`), `If-Match` (`found`,`row`), `return=minimal` (`n` [+`key`] [+`inserted`]) |
| `mut_bulk` | `{"n": int, "rows": [...]}` (`rows` absent under `return=minimal`) | bulk `PATCH`/`DELETE` by filter: `200` + `rows` (`[]` when `n = 0`), `204` under `return=minimal`; `n` feeds `max-affected` |

`found == false` → `404`; `row` null with `found == true` → `412`; `row` null
otherwise, or `n == 0` → `404`; `201` for `POST` and inserted `PUT` (with
`Location` from `key` when it has one column), else `200`, or `204` under
`return=minimal`. The wrapper key names are internal: goldens pin them,
clients never see them (resolved in M3).

Zero rows from `__mut` on a keyed path → `404` (mutation), transaction rolled
back (nothing to roll back, but the tx still ends in `ROLLBACK`).

## Transactions

- `transactions.mutations` (default `true`): every write (POST/PATCH/DELETE/PUT
  and volatile RPC) runs in `BEGIN … COMMIT` with `transactions.isolation`
  (`read_committed` default; `repeatable_read`, `serializable`), plus
  `SET LOCAL ROLE` / `set_config` when RLS claims are present. `false` → the
  statement autocommits (still one statement, still atomic per PG semantics;
  RLS claims force a tx anyway because `SET LOCAL` needs one).
- `transactions.max_retries` (default `0`): on SQLSTATE `40001`
  (serialization_failure) or `40P01` (deadlock_detected) the whole request is
  re-run — new tx, same statement, same params — up to N more times, no
  backoff. Exhausted → `409` with `code: pg:40001`/`pg:40P01`
  ([errors.md](errors.md#retries)).
- There is **no** `transactions.per_request`: a request is exactly one
  operation, so the flag would be meaningless. Nested mutations set `ForceTx`
  regardless of `transactions.mutations`.
- Hook errors from `RequestHook.BeforeRequest` abort before `BEGIN`.

## Method availability (`405` + `Allow`)

The router only registers write methods a resource supports; anything else on
a known path → `405` with an `Allow` header listing what exists:

| Condition | Effect |
|---|---|
| view / materialized view | `Allow: GET, HEAD` (writable views are not detected in v1) |
| no INSERT privilege | no `POST`, no `PUT` |
| no UPDATE privilege | no `PATCH`, no `PUT` |
| no DELETE privilege | no `DELETE` |
| no PK and no unique constraint | no `/{pk}`, no `/by/…` → no `PATCH`/`DELETE` at all |
| `@omit create|update|delete` (smart-comments) | method removed |
| `server.read_only: true` (or `App.SetReadOnly(true)` at runtime) | every write on every resource → `405 read_only`, `Allow: GET, HEAD` (`OPTIONS` when CORS is on); answered before the body is read |
| `PATCH`/`DELETE {base}/{table}` without a filter | not a `405`: `400 unfiltered_mutation` ([bulk writes](#bulk-patch--delete-by-filter)) |

Read-only mode also refuses volatile RPCs ([rpc.md](rpc.md#volatile-functions-are-mutations)).
Privileges are those of the connecting role at introspection time; RLS/`SET
ROLE` at request time may still yield `42501` → `403`.

## Nested mutations (plugin `nested-mutations`)

Built in M5 (`internal/plugins/nestedmutations`, priority 200, enabled by
default) on the core seams: `parse.Options.NestedKeys` (set from
`api.Surface.NestedKeys`, which the plugin's `APIHook` publishes → the body
parser stores claimed keys raw in `plan.Body.Nested[key].Raw`, single-object
`POST` only, and drops the FK columns of a forward relation from the required
set), `parse.CoerceObject` / `parse.CoerceValue` (body-rule coercion of nested
rows and `connect` keys), `compile.InputValue` (the core's value rendering:
composites, arrays, json, PostGIS constructors) and
`compile.MutationWithCTEs(req, ctes, ps, overrides)` (response compilation
over the plugin's CTE chain; the `%%RETURNING%%` placeholder in `__mut`
receives the narrow column list; `overrides` maps `"schema.table"` to a
visibility CTE).

`POST` create only (single object; bulk bodies never accept nested keys).
Relation keys inside a body use the same names as `select=` embeds (forward
FK → referenced table name, backward FK → referencing table name); `!hint`
is not accepted in bodies, so an ambiguous key (two FKs to the same table,
or a self-reference) is not offered at all until `@fieldName` /
`@foreignFieldName` gives each direction its own name.

| Direction | Body | Meaning |
|---|---|---|
| forward (`posts.author_id → users`) | `"users": {"create": {…}}` **or** `"users": {"connect": {"id": 7}}` | exactly one of the two → else `400 invalid_body`; `connect` keys must be exactly the primary key, one unique constraint or the referenced columns of `users` (`{"email": "…"}` works too); values non-null |
| backward (`users ← posts.author_id`) | `"posts": {"create": [{…}, {…}]}` | non-empty list of child rows; the FK columns are filled from the parent (`connect` here is v1.x) |

Rules:

- Depth ≤ `plugins.settings.nested-mutations.max_depth` (default 3; the root
  row is depth 1, every `create` — parent or child — adds one, `connect`
  adds none because it inserts nothing) → else `400 embed_depth_exceeded`
  (the code is shared with `select=` nesting; no plugin-specific code —
  resolved).
- Nested rows follow the body-validation table: unknown key `invalid_body`,
  generated column `column_readonly`, `NOT NULL` without default required
  (FK columns supplied by the parent or by a nested forward key excepted),
  values coerced per column type. Errors are prefixed with the body path:
  `nested "posts[1]": missing required column(s): title`,
  `nested "posts.users": …`.
- FK columns of a forward relation become optional in the body when the
  nested key is provided; providing both the FK column and the nested key →
  `400 invalid_body`. A child row cannot set the FK filled from its parent
  (nor carry a forward key over the same columns) → `400 invalid_body`.
- The request is forced into a transaction
  (`plugins.settings.nested-mutations.force_transactions`, default `true`,
  sets `exec.Operation.ForceTx` in `BeforeRequest`), even with
  `transactions.mutations: false`. The chain is one statement either way, so
  a failure never leaves partial rows; the transaction matters for the role
  / claim settings around it.
- `create` needs `POST` on the related resource (INSERT privilege, not
  `@omit create`) → else `400 unknown_relation`; `connect` only needs the
  resource to exist (SELECT). Keys that match no relation are plain unknown
  body keys (`400 invalid_body`), exactly as with the plugin disabled.
- OpenAPI: the plugin publishes `api.Surface.NestedKeys` (the per-resource
  key → relation map M6 renders into the `.insert` schemas) and a
  `<name>.connect` schema per connectable resource
  ([openapi.md](openapi.md)).

Shape — parents first (`__p_n`), then the main row (`__mut`), then children
(`__c_n`), then one visibility CTE (`__vis_n`) per table that received
inserts; one counter numbers them in emission order. Children are inserted
from `SELECT … FROM <parent cte>` so FK values come from the parent's
`RETURNING`:

```http
POST /api/users?select=id,email,posts(id,title)
Content-Type: application/json

{"email": "a@b.c", "posts": {"create": [{"title": "One"}, {"title": "Two"}]}}
```

```sql
WITH __mut AS (
  INSERT INTO "public"."users" ("email")
  VALUES ($1)
  RETURNING "id", "email"
),
__c_1 AS (
  INSERT INTO "public"."posts" ("title", "author_id")
  SELECT $2, __mut."id"
  FROM __mut
  RETURNING *
),
__c_2 AS (
  INSERT INTO "public"."posts" ("title", "author_id")
  SELECT $3, __mut."id"
  FROM __mut
  RETURNING *
),
__vis_3 AS (
  SELECT * FROM __c_1 UNION ALL SELECT * FROM __c_2
)
SELECT jsonb_build_object('row', jsonb_build_object('id', t0."id", 'email', t0."email", 'posts', l3.data), 'key', jsonb_build_array(t0."id")) AS data
FROM __mut t0
LEFT JOIN LATERAL (
  SELECT coalesce(jsonb_agg(s2.data), '[]'::jsonb) AS data
  FROM (
    SELECT jsonb_build_object('id', t1."id", 'title', t1."title") AS data
    FROM __vis_3 t1
    WHERE t1."author_id" = t0."id"
    ORDER BY t1."id" ASC
    LIMIT $4
  ) s2
) l3 ON true
-- args: ["a@b.c","One","Two",100]
```

(goldens `nested_children`, `nested_create_parent`, `nested_connect`.)

Forward `create` inserts the parent in `__p_n AS (INSERT … RETURNING *)`
and the main insert reads its key: `INSERT INTO "public"."posts" ("title",
"author_id") SELECT $3, __p_1."id" FROM __p_1`; `connect` replaces the
insert with `__p_n AS (SELECT * FROM "public"."users" WHERE "id" = $1)` —
zero rows there means zero rows inserted → `404 not_found` for the whole
request (also under `Prefer: return=minimal`). Column values go through the
core's `inputValue` (`compile.InputValue`), so composites, arrays, `jsonb`
and PostGIS GeoJSON/WKT inside nested rows render exactly as in a plain
`POST`; the SQL text stays a pure function of the body shape.

Visibility: rows written by a CTE are invisible to plain table scans in the
same statement, so the response reads every table that received nested
inserts — children **and** created parents — from a `UNION ALL` of its
insert CTEs (`__vis_n`, passed as `MutationWithCTEs` overrides) instead of
the table. Only rows created in this request appear in those embeds; a
`connect`ed parent is read from the table as usual. Nested CTEs use
`RETURNING *` (they are internal row sources); the column-grant-friendly
narrow `RETURNING` applies to `__mut` and the outer response only.

Atomicity — a child failure rolls back the parent:

```http
POST /api/users
{"email": "a@b.c", "posts": {"create": [{"title": "ok"}, {"title": null}]}}
```

```http
HTTP/1.1 400 Bad Request
Content-Type: application/problem+json

{"type":"about:blank","title":"Bad Request","status":400,"detail":"null value in column \"title\" of relation \"posts\" violates not-null constraint","instance":"/api/users","code":"pg:23502"}
```

No `users` row exists afterwards: one statement, one transaction
(`test/e2e_nestedmutations_test.go` checks it with and without
`force_transactions`).

## RLS caveats for `RETURNING`

PostgreSQL evaluates `RETURNING` with the same privilege rules as a `SELECT`:
each projected column needs a column-level `SELECT` grant, and (with RLS
enabled) each returned row must pass the table's `SELECT` policies. Two
consequences:

- **Column-level grants.** pdbr compiles `RETURNING` from the `select=` list
  (or all columns when absent), so a role granted `SELECT` on a subset of
  columns can run mutations as long as `select=` stays within that subset.
  Exceptions that need every column: computed columns in `select=` (row-type
  argument), `If-Match` (hashes the whole row), and nested-mutation inner
  CTEs (`RETURNING *`).

- **`SELECT` policies apply to rows returned by the mutation itself.** All
  nested inserts run in one statement, and rows inserted by an earlier CTE
  are not visible to a policy's subquery in the same snapshot. A child-table
  `SELECT` policy that `EXISTS`-checks its parent row can never pass when the
  parent was created in the same statement — the insert succeeds but the
  `RETURNING` row is filtered, which PostgreSQL reports as an error
  (`42501` → `403`). Write such policies against values that don't need
  same-statement visibility (compare the FK column to a claim setting
  directly), or split the creates into separate requests.

- An `UPDATE`/`DELETE` whose row is hidden by a `SELECT`/`UPDATE` policy
  matches zero rows → `404`, indistinguishable from a missing row by design.

See [rls.md](rls.md) for the claim mechanism.

## Deferred to v1.x

- `limit`/`order` on bulk `PATCH`/`DELETE` (PostgREST's limited update/delete).
- Nested `connect` on backward relations (`"posts": {"connect": [{"id": 1}]}`).
- Nested update / delete / disconnect.
- Savepoints (`SAVEPOINT` per nested CTE for partial success).
- Writable views (`INSTEAD OF` triggers / auto-updatable views).
- `Prefer: resolution=merge-duplicates|ignore-duplicates`, `Prefer: tx=`
  (rejected under `handling=strict`).
- `ETag` on mutation responses.
