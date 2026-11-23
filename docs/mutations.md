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
| `PUT {base}/{table}/{pk}` · `…/by/{cols}/{vals}` | `INSERT … ON CONFLICT (<path key>) DO UPDATE` | `201` inserted / `200` updated ([single-row PUT](#put-on-a-single-row-path)) |
| `POST {base}/{table}` + `Prefer: resolution=` | `INSERT … ON CONFLICT DO UPDATE` / `DO NOTHING` | `201` ([resolution](#post-with-prefer-resolution)) |
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
SELECT json_build_object('row', json_build_object('id', t0."id", 'email', t0."email", 'full_name', t0."full_name", 'mood', t0."mood", …), 'key', jsonb_build_array(t0."id")) AS data
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
SELECT coalesce(json_agg(s2.data), '[]'::json) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'email', t0."email", 'full_name', t0."full_name", …) AS data
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
SELECT json_build_object('id', t0."id", …) AS data
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
SELECT json_build_object('id', t0."id", 'author_id', t0."author_id", 'title', t0."title", 'body', t0."body", 'published', t0."published") AS data
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
SELECT json_build_object('n', count(*), 'rows', coalesce(json_agg(s3.data), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t1."id", …) AS data
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
| `resolution=merge-duplicates\|ignore-duplicates` | `POST` | upsert / skip conflicting rows ([above](#post-with-prefer-resolution)) |
| `missing=default` | `POST`, `PUT` | accepted and echoed; pdbr always defaults omitted keys |
| `count=exact\|planned\|estimated` | bulk `PATCH`, `DELETE` | `Content-Range: 0-<n-1>/<n>` (the written-row count is always exact, so `Preference-Applied` says `count=exact`); lists: [pagination.md](pagination.md#prefer-count-and-content-range) |
| `tx=commit\|rollback` | writes (incl. volatile RPCs) | only with `server.tx_end: commit-allow-override` or `rollback-allow-override`: the request runs in a transaction that is committed or rolled back; a rollback still answers with the normal result (status, body, `Location`) and writes nothing. Otherwise ignored, or `400 invalid_preference` under `handling=strict`. Echoed as `tx=<value>` |
| `partial=items` | bulk `POST`, `PUT` | only with `server.partial_bulk_writes: true`: every element runs in its own savepoint and the response is a per-element report ([below](#partial-success-for-bulk-writes)). Ignored (not echoed) for a single object, `PATCH` and `DELETE`; ignored while the config is off, or `400 invalid_preference` under `handling=strict`. Echoed as `partial=items` |
| `timezone=<name>` | any request | sets the transaction's `timezone` (`set_config('timezone', $1, true)`, bound) so `timestamptz` output and `now()` use it. Names are matched case-insensitively against `pg_timezone_names` (loaded once, cached); unknown → ignored, or `400 invalid_preference` under `handling=strict`. Echoed as `timezone=<canonical name>` |
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

`tx=rollback` (or `server.tx_end: rollback`) is also a single-statement
request: the batch is sent as `BEGIN`, the statement, and the transaction is
rolled back once its result has been read (a second round trip, like
`max-affected`). A statement that fails still answers with its error.

A `timezone=` preference adds one constant statement,
`SELECT set_config('timezone', $1, true)`, before the request's own in the
same transaction batch.

### Partial success for bulk writes

A bulk `POST` or `PUT` (a body array) is one `INSERT` and all-or-nothing:
one bad element rolls the whole body back. With `server.partial_bulk_writes:
true` (default `false`) a request can opt in per request:

```
POST /api/users
Prefer: partial=items

[{"email":"a@x.io"},{"email":"a@x.io"},{"email":"c@x.io"}]
```

```
HTTP/1.1 207 Multi-Status
Preference-Applied: partial=items

{"n":2,"failed":1,"items":[
  {"index":0,"status":201,"row":{"id":7,"email":"a@x.io"}},
  {"index":1,"status":409,"code":"pg:23505","detail":"duplicate key value violates unique constraint \"users_email_key\""},
  {"index":2,"status":201,"row":{"id":8,"email":"c@x.io"}}]}
```

PostgREST has no equivalent preference (its bulk writes are atomic), so
`partial=items` is pdbr-specific. It follows the `Prefer` grammar: a token
with a value, matched case-insensitively and echoed in `Preference-Applied`
in header order.

- **Granularity.** Each element is compiled and run as a statement of its
  own, in body order, inside a `SAVEPOINT`: a failing element is rolled back
  to its savepoint and reported, the others commit with the transaction.
  `PATCH` and `DELETE` write one statement chosen by a filter, so there is
  nothing to split and the token does not apply to them. Body validation
  (unknown keys, missing required columns, conflict targets) still rejects
  the whole request with `400` before anything runs.
- **Response.** `n` counts the elements that succeeded and `failed` the
  others. Each item has its `index` in the body and the `status` the element
  would have had as a request of its own: `201` (`200` for `PUT`) with the
  written `row` (no `row` under `return=minimal`), `204` for an element
  `resolution=ignore-duplicates` skipped, or the error's status with its
  `code` and `detail` (and `pg` under `errors.detail: dev`) as in a
  [problem document](errors.md). The HTTP status is `201` (`200` for `PUT`)
  when every element succeeded and `207` when at least one failed. A client
  must read `items`, not the status alone: `207` still committed the
  elements that succeeded.
- **Single-statement invariant.** Partial mode is the one exception to
  "everything is one statement per request": it uses `pgexec.RunTx` and costs
  a round trip for the `SAVEPOINT`, one for the element's statement and one
  for `RELEASE` per element (plus `ROLLBACK TO` for a failed one), instead of
  one pipelined batch. The body is bounded by `server.max_body_bytes` and the
  request by `server.request_timeout`.
- **Retries.** A serialization failure or deadlock is never an element
  failure: the transaction aborts and the whole request re-runs, up to
  `transactions.max_retries` times. When the retries run out, or `COMMIT`
  fails, nothing was written and the answer is the plain `409`
  (`pg:40001` / `pg:40P01`), not a report.
- **RLS identity.** `SET LOCAL ROLE` and the claims are applied once per
  transaction and hold for every element; a savepoint rollback does not
  undo them. An RLS violation in one element is that element's `403`
  (`pg:42501`) and the others commit.
- **`tx=rollback`.** Under `server.tx_end: rollback*` or an honoured
  `Prefer: tx=rollback`, every element runs and is reported as usual, then
  the transaction is rolled back: the report is a dry run and nothing is
  written.
- **`max-affected`.** Applies to `PATCH` and `DELETE` only, so it never
  combines with `partial=items`.
- **`transactions.mutations: false`** does not matter: a partial request
  always runs in a transaction.
- `--print-sql` and `Prepare` show the compiled statement of the whole body,
  not the per-element statements.

### `columns=`

`POST`, `PUT` and `PATCH` (bulk and keyed) accept `?columns=a,b,c`, the body
keys the write uses (response keys, comma-separated). Every other key in the
body is ignored, even an unknown one. The written columns are the listed keys
present in at least one element; an element that omits one takes the column
`DEFAULT` (so heterogeneous bulk bodies work as before, and `columns=` also
pins which keys of a wider payload reach the table). A listed key that is not
a writable column is `400 invalid_body` (`column_readonly` for a generated
one). The required-column, conflict-target and non-empty-`PATCH` rules apply
to the filtered body. `columns=` is not reserved on `GET`, `DELETE` or RPCs.

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
SELECT json_build_object('row', json_build_object('id', t0."id", …), 'inserted', t0.__inserted, 'key', jsonb_build_array(t0."id")) AS data
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
| temporal keys (PostgreSQL 18 `WITHOUT OVERLAPS`) | never a conflict target: `ON CONFLICT` cannot infer the exclusion constraint behind them. A temporal PK is skipped as the default (`?on_conflict=` must name a plain unique constraint, else `400 temporal_conflict_target`), and a table whose only keys are temporal has no `PUT` (`405`) |
| `?on_conflict=col1,col2` | column set (order-insensitive) must match a unique constraint or the PK → else `400 invalid_body`; naming a temporal key → `400 temporal_conflict_target` |
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

## PUT on a single-row path

`PUT {base}/{table}/{pk}` and `PUT {base}/{table}/by/{cols}/{vals}` upsert
the one row the path names: the path's constraint (primary key or that
unique constraint) is the conflict target, and the key values come from the
path.

```http
PUT /api/users/by/email/a%40b.c?select=id,full_name
Content-Type: application/json

{"full_name": "Ada"}
```

```sql
WITH __mut AS (
  INSERT INTO "public"."users" ("email", "full_name")
  VALUES ($1, $2)
  ON CONFLICT ("email") DO UPDATE
  SET "full_name" = EXCLUDED."full_name"
  RETURNING "id", "full_name", (xmax = 0) AS __inserted
)
SELECT json_build_object('row', json_build_object('id', t0."id", 'full_name', t0."full_name"), 'inserted', t0.__inserted, 'key', jsonb_build_array(t0."id")) AS data
FROM __mut t0
-- args: ["a@b.c","Ada"]
```

| Rule | Behaviour |
|---|---|
| key columns in the body | optional; when present they must equal the path values (compared after coercion) → else `400 invalid_body` |
| body | one JSON object (arrays → `400 invalid_body`); `NOT NULL` columns without a default are required as for an insert |
| other columns | provided ones are set on conflict; unprovided ones keep their values (same partial semantics as the collection `PUT`) |
| status | `201` + `Location` when inserted, `200` when updated, `204` under `return=minimal` (`201` + `Location` if inserted) |
| `?on_conflict=`, filters | `400 invalid_filter` (the path is the target) |
| `If-Match` | `400 if_match_unsupported` (use `PATCH` on the same path for a conditional update) |
| availability | wherever the collection `PUT` exists **and** every key column is writable by a `PUT` body: a key with an identity `ALWAYS` or generated column (the fixture's `users.id`), or one under `@omit create` / `@omit update`, has no `PUT` on that path (`405`), because inserting the path value would write a column bodies may never set; identity `BY DEFAULT` keys have one |

Golden files: `put_by_pk`, `put_by_unique`, `put_by_pk_mismatch`,
`put_by_pk_array`, `put_by_identity_pk`, `put_by_unique_on_conflict`.

## POST with `Prefer: resolution=`

`Prefer: resolution=merge-duplicates` turns a `POST` into an upsert and
`resolution=ignore-duplicates` skips rows that conflict, on `?on_conflict=`
(a unique constraint's columns) or the primary key:

```sql
-- POST /api/users?on_conflict=email
-- Prefer: resolution=ignore-duplicates, return=minimal
-- [{"email": "a@b.c"}, {"email": "d@e.f", "mood": "ok"}]
WITH __mut AS (
  INSERT INTO "public"."users" ("email", "mood")
  VALUES ($1, DEFAULT), ($2, $3)
  ON CONFLICT ("email") DO NOTHING
  RETURNING 1
)
SELECT json_build_object('n', (SELECT count(*) FROM __mut)) AS data
-- args: ["a@b.c","d@e.f","ok"]
```

| Rule | Behaviour |
|---|---|
| `merge-duplicates` | `ON CONFLICT (<target>) DO UPDATE SET c = EXCLUDED.c` for every provided non-target column (as `PUT`); the body is validated with the `PUT` rules, so a column under `@omit create` or `@omit update` is `400 invalid_body`; needs the resource's `PUT` method (INSERT + UPDATE and a key) → else `400 invalid_preference`; a PK-less table needs `?on_conflict=` → else `400 invalid_body` |
| `ignore-duplicates` | `ON CONFLICT (<target>) DO NOTHING`; a table with neither a PK nor `?on_conflict=` gets `ON CONFLICT DO NOTHING` (any unique constraint). The response holds only the inserted rows (PostgREST semantics); a skipped single object answers `204` without a body, on every table (also composite-key and PK-less ones) |
| temporal keys (`WITHOUT OVERLAPS`) | `ignore-duplicates` accepts a temporal PK (the default target) or a temporal unique in `?on_conflict=` and renders `ON CONFLICT ON CONSTRAINT "<name>" DO NOTHING`, the only form PostgreSQL accepts for the exclusion constraint behind it, so an overlapping row is skipped; `merge-duplicates` on a temporal target → `400 temporal_conflict_target` (`ON CONFLICT DO UPDATE` is not supported with exclusion constraints) |
| conflict columns | **not** required in the body (unlike `PUT`): an element without them is a plain insert (surrogate keys from their default) |
| status | `201` (single object also for a merge that updated, as in PostgREST), `Location` from the row's key |
| `?on_conflict=` without `resolution=` | `400 invalid_filter` |
| nested-mutation keys | `400 invalid_body` (nested writes cannot be combined with conflict resolution) |
| bulk gaps | missing keys insert `DEFAULT`; with `merge-duplicates` a gap on a conflicting row therefore writes the column default (`EXCLUDED.c` is the default), exactly like PostgREST with `missing=default` |

`Prefer: missing=default` is accepted on `POST`/`PUT` and echoed in
`Preference-Applied`, but changes nothing: pdbr always inserts `DEFAULT`
for keys an element omits (PostgREST inserts `NULL` unless
`missing=default` is given).

Golden files: `post_merge_duplicates`, `post_ignore_duplicates`,
`post_ignore_pk`, `post_merge_identity_pk`,
`post_on_conflict_without_resolution`.

## `select=` on mutation responses

`select=` (same grammar as [reads](api.md#select), including embeds and aliases) shapes the
returned row. It compiles against the `__mut` CTE exactly like a table row
source, so embeds are `LEFT JOIN LATERAL` subqueries correlated on `__mut`
columns (against the real tables, except for the mutated table itself, see
[embeds back to the mutated table](#embeds-back-to-the-mutated-table)):

```http
POST /api/posts?select=id,title,users(id,email)
```

```sql
WITH __mut AS (
  INSERT INTO "public"."posts" ("author_id", "title")
  VALUES ($1, $2)
  RETURNING "id", "author_id", "title"
)
SELECT json_build_object('row', json_build_object('id', t0."id", 'title', t0."title", 'users', l2.data), 'key', jsonb_build_array(t0."id")) AS data
FROM __mut t0
LEFT JOIN LATERAL (
  SELECT json_build_object('id', t1."id", 'email', t1."email") AS data
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

### Embeds back to the mutated table

Every table scan of the statement shares one snapshot, so a plain embed that
leads back to the mutated table (`POST /posts?select=id,users(posts(id))`)
would read it as it stood **before** the DML: the new post missing, the old
version of a patched row, a deleted row still listed. Such embeds (and the
embed filters, orders and relation filters inside them) read a visibility CTE
instead, the table as it stands after the statement:

```sql
__vis AS NOT MATERIALIZED (
  SELECT <cols> FROM t WHERE <rows the DML left alone>
  UNION ALL SELECT __mut.<cols> FROM __mut      -- not for DELETE
)
```

| Mutation | Rows of `t` kept (the pre-statement versions of the replaced rows go) |
|---|---|
| `POST` (single, bulk, `resolution=ignore-duplicates`) | all |
| keyed `PATCH`/`DELETE` (PK or `/by/…`, with or without `If-Match`) | `(<the DML's own key lookup>) IS NOT TRUE`, placeholders reused, so it still matches the old version when the patch changes the key |
| single-object `PUT` / `POST` + `resolution=merge-duplicates` | `(<conflict key = input>) IS NOT TRUE` |
| bulk `PUT` / merge-duplicates (or a conflict column left to its default) | `NOT EXISTS (SELECT 1 FROM __mut WHERE __mut.k = t.k)` over the conflict key |
| bulk `PATCH`/`DELETE` by filter | `NOT EXISTS (…)` over the first `NOT NULL` row key (PK, then unique constraints) the patch leaves alone: exact even when an `UPDATE`/`DELETE` policy skips rows the filter matched. Only when every such key is patched (or there is none) does it fall back to `(<filter>) IS NOT TRUE`, which hides such skipped rows from the embed |

```http
PATCH /api/posts/1?select=id,title,users(posts(id,title))
```

```sql
WITH __mut AS (
  UPDATE "public"."posts"
  SET "title" = $1
  WHERE "public"."posts"."id" = $2
  RETURNING "id", "author_id", "title"
),
__vis AS NOT MATERIALIZED (
  SELECT "id", "author_id", "title" FROM "public"."posts" WHERE ("public"."posts"."id" = $2) IS NOT TRUE
  UNION ALL SELECT __mut."id", __mut."author_id", __mut."title" FROM __mut
)
SELECT json_build_object('id', t0."id", 'title', t0."title", 'users', l5.data) AS data
FROM __mut t0
LEFT JOIN LATERAL (
  SELECT json_build_object('posts', l4.data) AS data
  FROM "public"."users" t1
  LEFT JOIN LATERAL (
    … FROM __vis t2 WHERE t2."author_id" = t1."id" …
  ) l4 ON true
  WHERE t1."id" = t0."author_id"
  LIMIT 1
) l5 ON true
```

- The CTE is emitted only when the response reads the mutated table through
  an embed; every other mutation compiles to the same SQL as before.
- It projects only the columns read through it, and `RETURNING` widens to
  them (plus the key a bulk exclusion reads from `__mut`), so column-level
  grants keep working ([RLS caveats](#rls-caveats-for-returning)).
- RLS: the table half runs under the role's policies; `RETURNING` rows have
  already passed the `SELECT` policies.
- `NOT MATERIALIZED` inlines it at every reference, so the correlated embed
  predicates still reach the table's indexes. No `RETURNING old.*`: works on
  PostgreSQL 14+.
- Computed columns evaluated inside embeds see the merged row, but a
  computed function that scans the mutated table itself
  (`users_post_count`) still runs in the statement's snapshot.
- Aggregates (`count()`) are refused on mutation responses, as before.

(goldens `mutvis_*`; e2e `test/e2e_mutation_visibility_test.go`.)

## Body validation

Validation happens in the parser (`internal/parse/body.go`) against the
catalog; failures are `400` before any SQL runs. Bulk element errors are
prefixed `element N:` and keep their code.

| Rule | Error |
|---|---|
| top-level must be an object (POST/PUT: object or non-empty array of objects) | `invalid_body` |
| key that is not a column of the resource (or a nested-mutation relation key) | `400 invalid_body` |
| nested-mutation operation the relation does not offer (no privilege, `@omit create\|update\|delete`, FK column not writable, referenced column hidden; see [Availability](#availability-omit-per-action)) | `400 unknown_relation` |
| generated (`GENERATED ALWAYS AS … STORED`) or identity `ALWAYS` column | `400 column_readonly` |
| POST: `NOT NULL && !HasDefault` column missing | `400 invalid_body` listing the missing columns |
| PATCH: no required keys; empty patch | `400 invalid_body` |
| PUT: conflict-target columns missing | `400 invalid_body` |
| value not coercible to the column type (e.g. object for `int4`) | `400 invalid_body` |

Identity `BY DEFAULT` columns are writable, like serial columns: optional
on `POST` (an omitted value takes the sequence's next one), settable by
`PATCH` and `PUT`, and usable as a `PUT` conflict target.

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
| `hstore` | object of string → string\|null → `"ext".hstore($n::text[])`, or an hstore literal string — [contrib.md](contrib.md#input-post--put--patch-bodies) |
| ranges / multiranges | literal string (`"[1,5)"`), or `{"start","end","startInclusive","endInclusive","isEmpty"}` (multiranges: an array of those or literals) → the type's constructor over bind parameters |
| `null` | SQL `NULL` (POST: only valid for nullable columns, else `23502` → `400`); the SQL text is the same as for a non-null value |

Unknown or unmapped PG types accept a JSON string (or number/bool, rendered
as text) passed as a bare `$n` typed by the target column.

## If-Match on PATCH / DELETE

Single-row GETs return `ETag: W/"<hex>"` = `md5(to_jsonb(ROW(…))::text)` over the
exposed columns ([api.md](api.md#etag--conditional-requests), `server.etags`). `PATCH`/`DELETE` on single-row paths
honour `If-Match` in the same statement — no read-then-write race:

```http
PATCH /api/users/1
If-Match: W/"5d41402abc4b2a76b9719d911017c592"

{"full_name": "Grace"}
```

```sql
WITH cur AS (
  SELECT md5(to_jsonb(ROW(__cur."id", __cur."email", __cur."full_name", __cur."mood", __cur."settings", __cur."tags", __cur."balance", __cur."created_at", __cur."address", __cur."prev_addresses", __cur."moods"))::text) AS h
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
SELECT json_build_object('found', EXISTS (SELECT 1 FROM cur), 'row', (
  SELECT json_build_object('id', t0."id", …)
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

Mutation responses do not carry an `ETag` header (a
second hash over the post-mutation row is cheap; revisit if clients want
PATCH → PATCH chains without an intermediate GET).

## SQL shape summary

Every mutation statement is `WITH __mut AS (<DML> RETURNING <cols>) SELECT
<jsonb> AS data`. The outer `SELECT` is the same row/list compiler used by
reads, with `__mut` as the row source; row JSON is `json_build_object`
(chunked ≤ 40 pairs, `||`-concatenated), lists are `coalesce(json_agg(…),
'[]'::json)`.

When Go needs a value that is not part of the body (the primary key for
`Location`, upsert `inserted`, `If-Match` `found`, the affected count under
`return=minimal`), the statement wraps the body in one object and the server
unwraps `row`. As built there are four mutation shapes
(`compile.Statement.Shape`, unwrapped in `internal/exec/unwrap_mutation.go`):

| Shape | `data` | Used for |
|---|---|---|
| `mut_row` | the row JSON verbatim; zero rows → `404` | keyed `PATCH`/`DELETE` with `return=representation` and no `If-Match` |
| `mut_list` | the JSON array verbatim (`[]` when empty) | bulk `POST` (`201`) / bulk `PUT` (`200`) |
| `mut_wrap` | `{"found": bool, "inserted": bool, "key": [pk], "n": int, "row": obj\|null}` — members present per request, `row` last | single `POST` (`key`,`row`), single `PUT` (`inserted`,`key`,`row`), `If-Match` (`found`,`row`), `return=minimal` (`n` [+`key`] [+`inserted`]) |
| `mut_bulk` | `{"n": int, "rows": [...]}` (`rows` absent under `return=minimal`) | bulk `PATCH`/`DELETE` by filter: `200` + `rows` (`[]` when `n = 0`), `204` under `return=minimal`; `n` feeds `max-affected` |

`found == false` → `404`; `row` null with `found == true` → `412`; `row` null
otherwise, or `n == 0` → `404`; `201` for `POST` and inserted `PUT` (with
`Location` from `key` when it has one column), else `200`, or `204` under
`return=minimal`. The wrapper key names are internal: goldens pin them,
clients never see them (resolved in M3).

Zero rows from `__mut` on a keyed path → `404` (mutation); the transaction
commits, having written nothing.

## Transactions

- `transactions.mutations` (default `true`): every write (POST/PATCH/DELETE/PUT
  and volatile RPC) runs in a transaction with `transactions.isolation`
  (`read_committed` default; `repeatable_read`, `serializable`), plus
  `SET LOCAL ROLE` / `set_config` when RLS claims are present. The
  transaction is one pipelined batch: under `read_committed` it is
  PostgreSQL's implicit pipeline transaction (no `BEGIN`/`COMMIT` sent, the
  server's `default_transaction_isolation` applies), the stricter levels
  send `BEGIN ISOLATION LEVEL …` and `COMMIT` in the same batch, and a
  `Prefer: max-affected` write commits only after its row-count check. `false` → the
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
| view / materialized view | `Allow: GET, HEAD` (writable views are not detected) |
| no INSERT privilege | no `POST`, no `PUT` |
| no UPDATE privilege | no `PATCH`, no `PUT` |
| no DELETE privilege | no `DELETE` |
| no PK and no unique constraint | no `/{pk}`, no `/by/…` → `PATCH`/`DELETE` only by filter on the collection |
| key with an identity `ALWAYS` / generated column | no `PUT` on that single-row path |
| only temporal keys (`WITHOUT OVERLAPS`) | same as no key: no `/{pk}`, no `/by/…`, no `PUT` ([api.md](api.md#temporal-keys-and-postgresql-18-foreign-keys)) |
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
parser stores claimed keys raw in `plan.Body.Nested[key].Raw` and drops the
FK columns of a forward relation from the required set),
`parse.CoerceObject` / `parse.CoerceObjectFor` / `parse.CoerceValue`
(body-rule coercion of nested rows, patches and keys), `compile.InputValue`
(the core's value rendering: composites, arrays, json, PostGIS
constructors), `compile.MutationChain` (response compilation over the
plugin's CTE chain: the `%%RETURNING%%` placeholder in `__mut` receives the
narrow column list, `Chain.Writes` tells the core which CTEs added, updated
or deleted rows of which table so it can build the visibility CTEs, and
`Chain.Guards` are evaluated before the response) and
`api.Surface.NestedSchema` (the OpenAPI body schemas).

Nested keys are accepted on a single-object `POST` and on `PATCH` of a
single-row path (`/{table}/{pk}`, `/{table}/by/{cols}/{vals}`). Bulk
`POST`, `PUT`, `PATCH` by filter and `POST` with `Prefer: resolution=`
treat them as unknown body keys (`400 invalid_body`), and `If-Match` on a
`PATCH` carrying them is `400 if_match_unsupported`. Relation keys inside
a body use the same names as `select=` embeds (forward FK → referenced
table name, backward FK → referencing table name); `!hint` is not accepted
in bodies, so an ambiguous key (two FKs to the same table, or a
self-reference) is not offered at all until `@fieldName` /
`@foreignFieldName` gives each direction its own name.

### Operations

A **forward** key (`posts.users`: the FK is on this row) takes an object
with one property per operation:

| Operation | Value | Created row | Updated row | Effect |
|---|---|---|---|---|
| `create` | the related `.insert` body (may nest) | ✓ | ✓ | insert the referenced row and point this row's FK at it |
| `connect` | key of an existing row | ✓ | ✓ | look the row up and point this row's FK at it |
| `disconnect` | `true` | | ✓ | set this row's FK to `NULL` (nullable FKs only) |
| `update` | the related `.update` body (may nest) | | ✓ | patch the row this row referenced **before** the request |
| `delete` | `true` | | ✓ | delete the row this row referenced before the request |

A created row takes exactly one of `create` / `connect`. An updated row
takes at most one of `create` / `connect` / `disconnect` (they set the FK),
plus `update` **or** `delete`. `delete` leaves this row's FK alone, so the
FK's `ON DELETE` action applies, and a `NO ACTION` / `RESTRICT` FK still
pointing at the row fails the request at the end of the statement (`409
pg:23503`); pair it with `disconnect` to clear a nullable FK in the same
request.

A **backward** key (`users.posts`: the FK points at this row) takes an
object of non-empty lists:

| Operation | Items | Created row | Updated row | Effect |
|---|---|---|---|---|
| `create` | the related `.insert` body (may nest) | ✓ | ✓ | insert rows referencing this row (their FK filled in) |
| `connect` | key of an existing row | ✓ | ✓ | point an existing row's FK at this row (it may reference another row, or none, before) |
| `update` | `{"where": <key>, "data": <.update body>}` | | ✓ | patch a row that references this row |
| `delete` | key | | ✓ | delete a row that references this row |
| `disconnect` | key | | ✓ | set the FK of a row that references this row to `NULL` (nullable FKs only) |

A **key** names exactly the primary key or the columns of one unique
constraint of the related resource (`{"id": 7}`, `{"email": "…"}`), with
non-null values; a forward `connect` also accepts the FK's referenced
columns (and, over a `PERIOD` FK, the referenced entity: see
[Temporal relations](#temporal-period-relations)). Columns hidden by
`@omit read` cannot form a key.

```http
PATCH /api/users/1?select=id,full_name,places(id,name)
Content-Type: application/json

{"full_name": "Ada",
 "places": {"connect": [{"id": 3}],
            "delete": [{"id": 2}],
            "update": [{"where": {"id": 1}, "data": {"name": "Trafalgar"}}]}}
```

```http
PATCH /api/places/4
Content-Type: application/json

{"name": "Opera House", "users": {"disconnect": true}}
```

```http
POST /api/users?select=id,posts(id,title)
Content-Type: application/json

{"email": "a@b.c", "posts": {"connect": [{"id": 3}], "create": [{"title": "New"}]}}
```

### Availability (`@omit` per action)

An operation is offered only when the role may run it and the rows may be
written the way it writes them; otherwise it is `400 unknown_relation`, and
the OpenAPI schemas leave it out:

| Operation | Needs |
|---|---|
| `create` | `POST` on the related resource (INSERT privilege, not `@omit create`) |
| `update` | `PATCH` on the related resource (UPDATE privilege, not `@omit update`) |
| `delete` | `DELETE` on the related resource (DELETE privilege, not `@omit delete`) |
| backward `connect` / `disconnect` | `PATCH` on the related resource |
| every operation that sets an FK (`create`, `connect`, `disconnect`) | the FK columns writable by the body of the row holding them: `POST` columns for a row being created, `PATCH` columns for one being updated (an FK column under `@omit create` / `@omit update` removes them), and the referenced columns readable (an FK referencing a column under `@omit read` gets none: they would copy the hidden value into the readable FK column) |
| `disconnect` | every FK column nullable |

`@omit many` on a FK (or `@omit` on the FK, or either table) removes the
key itself. With contracts, each contract's API is built from its own
catalog copy, so a key or operation hidden there is absent from that
contract's bodies and schemas.

### Rules

- `connect`, `update` and `delete` must match a row, and so must the
  addressed row of a `PATCH` carrying nested keys: a key that matches
  nothing (missing, referencing another parent for a backward `update` /
  `delete`, not referenced by the row for a forward `update` / `delete`, or
  filtered out by RLS) fails the **whole** request with `404 not_found`,
  detail `nested "posts.update[0]": matched 0 rows` (or `row of "users":
  matched 0 rows` for the addressed row). A `disconnect` of a row that is
  not linked does nothing.
- Depth ≤ `plugins.settings.nested-mutations.max_depth` (default 3; the root
  row is depth 1, every `create` or `update` — of a parent or a child —
  adds one, `connect` / `delete` / `disconnect` add none) → else `400
  embed_depth_exceeded` (the code is shared with `select=` nesting).
- At most `plugins.settings.nested-mutations.max_operations` operations
  (default 50; every list item and every forward operation counts once)
  per request → else `400 invalid_body`.
- Nested rows and patches follow the body-validation table of their method
  (`POST` rules for `create`, `PATCH` rules for `update`): unknown key
  `invalid_body`, generated column `column_readonly`, `NOT NULL` without
  default required on `create` (FK columns supplied by the parent or by a
  nested forward key excepted), values coerced per column type. Errors are
  prefixed with the body path: `nested "posts[1]": missing required
  column(s): title`, `nested "posts.users": …`, `nested
  "places.update[0]": …`.
- FK columns of a forward relation become optional in a create body when the
  nested key is provided; providing both the FK column and the nested key →
  `400 invalid_body`. A child row cannot set the FK filled from its parent
  (nor carry a forward key over the same columns) → `400 invalid_body`; a
  backward `update`'s `data` cannot set the FK linking the child to this
  row (use `connect` / `disconnect`).
- A `PATCH` may not change a column that one of its backward keys matches
  the children on (the referenced key) → `400 invalid_body`.
- A row is modified at most once per statement (a PostgreSQL rule for
  data-modifying CTEs): two operations updating the same row leave only one
  change, and an operation that then finds nothing to update reports
  `matched 0 rows`.
- Referential actions are not reflected in the response: `ON DELETE
  CASCADE` / `SET NULL` / `SET DEFAULT` and `ON UPDATE CASCADE` run after
  the statement's last query, so rows they change still show as they were.
  The database is right; read again to see it.
- The request is forced into a transaction
  (`plugins.settings.nested-mutations.force_transactions`, default `true`,
  sets `exec.Operation.ForceTx` in `BeforeRequest`), even with
  `transactions.mutations: false`. The chain is one statement either way, so
  a failure never leaves partial rows; the transaction matters for the role
  / claim settings around it.
- Every operation runs as the request's role: `UPDATE` / `DELETE` policies
  narrow what `connect`, `update`, `delete` and `disconnect` can match (a
  filtered row is `404`, as above), `WITH CHECK` failures and `RETURNING`
  rows hidden by `SELECT` policies are `403 pg:42501`, and a `PATCH` that
  only carries nested keys still locks its row with `SELECT … FOR UPDATE`,
  which needs the `UPDATE` privilege and passes the `UPDATE` policies.
- Keys that match no relation are plain unknown body keys (`400
  invalid_body`), exactly as with the plugin disabled.
- OpenAPI: the `.insert` and `.update` schemas carry one property per
  nested key with the operations it offers there (`Surface.NestedSchema`),
  and `<name>.connect` describes the key of an existing row
  ([openapi.md](openapi.md)). `.update` is shared with `PATCH` by filter,
  which rejects nested keys; the property descriptions say so.

### SQL shape

Parents and forward operations first (`__p_n` for created, connected and
updated referenced rows, `__d_n` for a deleted one), then the main row
(`__mut`), then the backward operations (`__c_n`), all numbered by one
counter in emission order, then one visibility CTE per table the response
reads that the chain wrote (`__vis_n`, numbered in the order the response
first reads them; `__vis` for the root table), then `__guard` when an
operation must match a row. Children are inserted from `SELECT … FROM
<parent cte>` so FK values come from the parent's `RETURNING`:

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
__vis_1 AS NOT MATERIALIZED (
  SELECT "id", "author_id", "title" FROM "public"."posts"
  UNION ALL SELECT "id", "author_id", "title" FROM __c_1
  UNION ALL SELECT "id", "author_id", "title" FROM __c_2
)
SELECT json_build_object('key', jsonb_build_array(t0."id"), 'row', json_build_object('id', t0."id", 'email', t0."email", 'posts', l3.data)) AS data
FROM __mut t0
LEFT JOIN LATERAL (
  SELECT coalesce(json_agg(s2.data), '[]'::json) AS data
  FROM (
    SELECT json_build_object('id', t1."id", 'title', t1."title") AS data
    FROM __vis_1 t1
    WHERE t1."author_id" = t0."id"
    ORDER BY t1."id" ASC
    LIMIT 100
  ) s2
) l3 ON true
-- args: ["a@b.c","One","Two"]
```

A `PATCH` updates the addressed row in `__mut` and scopes every backward
operation to the rows referencing it (`UPDATE … FROM __mut WHERE <key> AND
<fk> = __mut."id"`, `DELETE … USING __mut …`); a backward `connect`
matches by key alone. The visibility CTE drops the rows the chain replaced
or deleted (by a `NOT NULL` key the operation left alone, matched against
its returned rows, or by the operation's own lookup when it changed every
such key) and adds the returned rows; the guards are evaluated in a
materialized CTE scanned as the outer side of a lateral join with the
response, so they run even when the response is empty, and a failing guard
raises inside the statement (`22P02` carrying `pdbr guard <token>: …`,
which the executor answers as `404 not_found`; `<token>` is a random
per-process secret, masked in `--print-sql` and goldens, so a `22P02`
quoting a client or stored value can never pass for a failed guard):

```http
PATCH /api/users/1?select=id,full_name,places(id,name)

{"full_name": "Ada", "places": {"connect": [{"id": 3}], "delete": [{"id": 2}], "update": [{"where": {"id": 1}, "data": {"name": "Trafalgar"}}]}}
```

```sql
WITH __mut AS (
  UPDATE "public"."users"
  SET "full_name" = $2
  WHERE "public"."users"."id" = $1
  RETURNING "id", "full_name"
),
__c_1 AS (
  UPDATE "public"."places"
  SET "owner_id" = __mut."id"
  FROM __mut
  WHERE "public"."places"."id" = $3
  RETURNING "public"."places".*
),
__c_2 AS (
  DELETE FROM "public"."places"
  USING __mut
  WHERE "public"."places"."id" = $4 AND "public"."places"."owner_id" = __mut."id"
  RETURNING "public"."places".*
),
__c_3 AS (
  UPDATE "public"."places"
  SET "name" = $6
  FROM __mut
  WHERE "public"."places"."id" = $5 AND "public"."places"."owner_id" = __mut."id"
  RETURNING "public"."places".*
),
__vis_1 AS NOT MATERIALIZED (
  SELECT "id", "owner_id", "name" FROM "public"."places" WHERE NOT EXISTS (SELECT 1 FROM __c_1 __m WHERE __m."id" = "public"."places"."id") AND NOT EXISTS (SELECT 1 FROM __c_2 __m WHERE __m."id" = "public"."places"."id") AND NOT EXISTS (SELECT 1 FROM __c_3 __m WHERE __m."id" = "public"."places"."id")
  UNION ALL SELECT "id", "owner_id", "name" FROM __c_1
  UNION ALL SELECT "id", "owner_id", "name" FROM __c_3
),
__guard AS MATERIALIZED (
  SELECT (SELECT CASE WHEN count(*) > 0 THEN true ELSE concat('pdbr guard <token>: row of "users": matched ', count(*), ' rows')::boolean END FROM __mut)
    AND (SELECT CASE WHEN count(*) > 0 THEN true ELSE concat('pdbr guard <token>: nested "places.connect[0]": matched ', count(*), ' rows')::boolean END FROM __c_1)
    AND (SELECT CASE WHEN count(*) > 0 THEN true ELSE concat('pdbr guard <token>: nested "places.delete[0]": matched ', count(*), ' rows')::boolean END FROM __c_2)
    AND (SELECT CASE WHEN count(*) > 0 THEN true ELSE concat('pdbr guard <token>: nested "places.update[0]": matched ', count(*), ' rows')::boolean END FROM __c_3) AS ok
)
SELECT __guarded.data
FROM __guard
LEFT JOIN LATERAL (
  SELECT json_build_object('id', t0."id", 'full_name', t0."full_name", 'places', l3.data) AS data
  FROM __mut t0
  LEFT JOIN LATERAL (
    SELECT coalesce(json_agg(s2.data), '[]'::json) AS data
    FROM (
      SELECT json_build_object('id', t1."id", 'name', t1."name") AS data
      FROM __vis_1 t1
      WHERE t1."owner_id" = t0."id"
      ORDER BY t1."id" ASC
      LIMIT 100
    ) s2
  ) l3 ON true
) __guarded ON true
WHERE __guard.ok
-- args: [1,"Ada",3,2,1,"Trafalgar"]
```

(goldens `nested_children`, `nested_create_parent`, `nested_connect`,
`nested_reverse_connect`, `nested_patch_children`, `nested_patch_disconnect`,
`nested_patch_forward_update`, `nested_patch_forward_connect`,
`nested_patch_forward_create`, `nested_patch_forward_disconnect`.)

Forward `create` inserts the parent in `__p_n AS (INSERT … RETURNING *)`
and the main insert reads its key: `INSERT INTO "public"."posts" ("title",
"author_id") SELECT $3, __p_1."id" FROM __p_1`; `connect` replaces the
insert with `__p_n AS (SELECT * FROM "public"."users" WHERE "id" = $1)`
and a guard. In an `UPDATE` the FK is set from a scalar subquery
(`"owner_id" = (SELECT "id" FROM __p_1)`, plus `EXISTS (SELECT 1 FROM
__p_1)`), `disconnect` sets it to `NULL`, and a forward `update` / `delete`
targets the referenced row through the row as it was before the statement:
`WHERE EXISTS (SELECT 1 FROM "public"."posts" __o1 WHERE __o1."id" = $1 AND
__o1."author_id" = "public"."users"."id")`. A `PATCH` that sets no column
locks its row instead of updating it (`__mut AS (SELECT … FROM … WHERE …
FOR UPDATE)`; a nested `update` whose `data` only carries nested keys does
the same with `FOR UPDATE OF`). Column values go through the core's
`inputValue` (`compile.InputValue`), so composites, arrays, `jsonb` and
PostGIS GeoJSON/WKT inside nested rows render exactly as in a plain write;
the SQL text stays a pure function of the body shape.

Visibility: rows written by a CTE are invisible to plain table scans in the
same statement, so the response reads every table that the chain wrote —
children, created and updated parents, rows connected or disconnected —
from a visibility CTE: the table minus the rows the chain replaced or
deleted, `UNION ALL` the rows it inserted or updated (`__vis_n`, built by
the core from `Chain.Writes`), projected to the columns the response reads.
Embeds back to the root table read `__vis` (table plus `__mut`, plus any
nested write to the same table), exactly as for a
[plain mutation](#embeds-back-to-the-mutated-table). A `connect`ed parent is
an existing row and appears through the table half. Nested CTEs return
every column (`RETURNING *` / `RETURNING "schema"."table".*`: they are
internal row sources); the column-grant-friendly narrow `RETURNING` applies
to `__mut` and the outer response only.

### Temporal (`PERIOD`) relations

A PostgreSQL 18 `PERIOD` foreign key, `FOREIGN KEY (room_id, PERIOD
valid_at) REFERENCES room_bookings (room_id, PERIOD valid_at)`, relates a
row to every referenced row whose period overlaps its own, so a nested write
copies only the **equality** columns (`room_id`); the period column is
always the referencing row's own and stays a required body column. Whether
the referenced periods cover it is PostgreSQL's check (`409 pg:23503`
otherwise).

| Direction | Body | Meaning |
|---|---|---|
| forward `connect` | `"room_bookings": {"connect": {"room_id": 1}}` | names the referenced entity: the equality columns of the referenced key (or one of its plain keys); `__p_n AS (SELECT DISTINCT "room_id" FROM … WHERE "room_id" = $1)`, so one row is inserted however many periods match |
| forward `create` | `"room_bookings": {"create": {"room_id": 1, "valid_at": "…", …}}` | inserts the parent; the child takes `room_id` from it and keeps its own `valid_at`. The response's embed reads the table **and** the new parent (`__vis_n AS NOT MATERIALIZED (SELECT … FROM "public"."room_bookings" UNION ALL SELECT … FROM __p_1)`, like every visibility CTE), because the child may overlap existing periods too |
| backward `create` | `"booking_notes": {"create": [{"note": "…", "valid_at": "…"}]}` | children take `room_id` from the new parent, each with its own period |
| forward `update` / `delete` (on `PATCH`) | `"room_bookings": {"update": {"guest": "…"}}` | reach every referenced row whose period overlaps the row's own (`__o1."room_id" = "room_bookings"."room_id" AND __o1."valid_at" && "room_bookings"."valid_at"`) |
| backward `update` / `delete` / `disconnect` (on `PATCH`) | `"booking_notes": {"delete": [{"id": 4}]}` | reach the children whose period overlaps the parent's; a row is named by a plain key, so a table with only a temporal key cannot be a backward `connect` / `update` / `delete` / `disconnect` target (`400 invalid_body`) |

(goldens `temporal_nested_connect`, `temporal_nested_create_parent`,
`temporal_nested_children`, `temporal_nested_patch_forward_update`,
`temporal_nested_patch_backward_connect`.)

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
`force_transactions`; `test/e2e_nested_writes_test.go` checks a `PATCH`
whose nested operation fails, by a database error or a guard).

## RLS caveats for `RETURNING`

PostgreSQL evaluates `RETURNING` with the same privilege rules as a `SELECT`:
each projected column needs a column-level `SELECT` grant, and (with RLS
enabled) each returned row must pass the table's `SELECT` policies. Two
consequences:

- **Column-level grants.** pdbr compiles `RETURNING` from the `select=` list
  (or all columns when absent), so a role granted `SELECT` on a subset of
  columns can run mutations as long as `select=` stays within that subset.
  An embed back to the mutated table adds the columns it reads (and, for
  bulk `PATCH`/`DELETE` and bulk upserts, the key identifying the written
  rows) to both `RETURNING` and the [visibility CTE](#embeds-back-to-the-mutated-table).
  Exceptions that need every column: computed columns in `select=` (row-type
  argument), `If-Match` (hashes every exposed column), and nested-mutation inner
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

## Not yet supported

- `limit`/`order` on bulk `PATCH`/`DELETE` (PostgREST's limited update/delete).
- Savepoints (`SAVEPOINT` per nested CTE for partial success).
- Writable views (`INSTEAD OF` triggers / auto-updatable views).
- `ETag` on mutation responses.
