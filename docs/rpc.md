# RPC (functions) and computed columns

As built (M4). Every function in `schema.schemas` that
passes the [exclusion rules](#exclusion-rules) becomes an RPC route; functions
whose first argument is a table's row type instead become
[computed columns](#computed-columns). `schema.functions: false` disables
both. One request → one statement → one jsonb `data` row, as everywhere else.

| Route | Volatility | Args | Semantics |
|---|---|---|---|
| `GET {base}/rpc/{fn}?arg=value…` | `stable` / `immutable` | query string | read; runs outside a tx unless RLS claims/role are present |
| `POST {base}/rpc/{fn}` (JSON body `{"arg": value}`) | any | body | volatile → [mutation semantics](#volatile-functions-are-mutations); stable/immutable via POST behave like GET |
| `GET` on a volatile function | — | — | `405`, `Allow: POST` |
| `{base}/{schema}/rpc/{fn}` | | | schema-qualified form; bare `/rpc/{fn}` alias only for `schema.schemas[0]` |

`HEAD` mirrors `GET`. Unknown function → `404 not_found`. Examples use the
fixture functions (`add_numbers(a int4, b int4)` — **volatile** in the
fixture, so `POST` only —, `search_posts(term text) SETOF posts`,
`publish_post(post_id int4) posts`, `retry_probe()`, `describe_mood(m mood)`,
`mood_moods() SETOF mood`, `users_post_count(users)`,
`posts_excerpt(posts,int4)`).

## Arguments

- Mapped **by name** to the function's declared argument names (verbatim, no
  inflection). Every argument is optional at the HTTP level; a missing
  argument is passed as `NULL::<type>` (pdbq parity — PG defaults are *not*
  applied because a positional call with explicit NULLs is what the statement
  emits). A function that needs its default must be called with the value.
- Unknown argument name → `400 unknown_argument` (see [errors.md](errors.md#code-vocabulary)).
- On GET, query params that are RPC arguments are consumed as arguments; the
  reserved names (`select`, `order`, `limit`, `offset`, `count`, …) keep their
  meaning for `SETOF <table>` returns. A function argument literally named
  `select` etc. is only reachable via POST (documented limitation, same as the
  reserved-filter-name rule on tables).
- On GET, `SETOF <table>` functions also accept filters (`col=op.value`);
  precedence: reserved param → argument name → filter column. An argument and
  a return column with the same name → the argument wins on GET, use POST +
  `?col=…` to filter.

  Settled: GET-side precedence is reserved → argument → filter column
  (PostgREST does the same). A GET parameter that is neither an argument
  nor (for `SETOF <table>`) a column follows the filter grammar's error
  (`unknown_column` / `invalid_filter`); on functions without a table
  return it is `unknown_argument`.

- Coercion by PG type of the argument (values are `$n::<type>` parameters,
  schema-qualified for non-`pg_catalog` types: `$1::"public"."mood"`,
  arrays `$1::"text"[]`, enum arrays `$1::text[]::"public"."mood"[]`; a
  missing or JSON-`null` argument is the literal `NULL::<type>`):

| PG type | GET (`?arg=`) | POST body |
|---|---|---|
| numeric / bool / text-like | text, cast by PG | JSON scalar (`int8`/`numeric` number **or** string) |
| enum | label verbatim | label verbatim |
| array | `{a,b}` PG literal | JSON array |
| composite | JSON text | JSON object → `jsonb_populate_record(NULL::type, $n::jsonb)`; composite arrays → `ARRAY(SELECT jsonb_populate_recordset(NULL::type, $n::jsonb))` |
| `json`/`jsonb` | JSON text | any JSON value |
| geometry/geography (postgis plugin) | GeoJSON or WKT/EWKT text | GeoJSON object or WKT/EWKT string → `ST_GeomFromGeoJSON` / `ST_GeomFromEWKT` |
| any other | text `::type` | string `::type` |

- Uncoercible value → `400 invalid_body` (POST) / `400 invalid_filter` (GET)
  when detectable at compile time; otherwise PG's `22*` → `400 pg:22P02`.
- Body must be a JSON object (`{}` allowed → all `NULL`); non-object → `400
  invalid_body`; missing body on POST is treated as `{}`.

## Return shapes

| Function returns | Response body | Status | Extras |
|---|---|---|---|
| scalar (`int4`, `text`, `jsonb`, array, enum, …) | the JSON value (`42`, `"x"`, `[1,2]`, `null`) | `200` | `int8`/`numeric` as strings |
| `void` | none | `204` | |
| single row: table type or composite | object; `null` when the function returns NULL | `200` | `select=` (embeds for table types) |
| `SETOF <table>` | array of objects | `200` | `select=`, filters, `order=`, `limit`/`offset`, `count=true` → `X-Total-Count`, `Accept: text/csv`; **offset paging only** (no `after`/`before`, no cursors, `Link` with `offset=`) |
| `SETOF <scalar>` / `SETOF <composite>` | array of values / objects | `200` | `limit`/`offset` (`limit` defaults to and is capped by `server.max_page_size`; no `count`) |
| `rls.auth.jwt_type` composite (`SETOF` too) | signed HS256 JWT string (array of strings) | `200` | minted by `pdbcore/jwtmint` from the row's fields as claims |
| `TABLE(...)` / `RETURNS record` | | | excluded (OUT args) |

`SETOF <table>` results honour the table's filter/order policy
(`filters.indexed_only` etc.) — the function output is scanned, so the index
policy is advisory here, but the same rules keep the surface predictable.
`limit` defaults to and is capped by `server.max_page_size`.

Settled: `SETOF <scalar>` / `SETOF <composite>` get `limit`/`offset` but no
`count=true` (ignored) in v1.

## SQL shapes

Scalar:

```http
POST /api/rpc/add_numbers
{"a": 2, "b": 40}
```

```sql
SELECT to_jsonb(__fn.v) AS data
FROM (SELECT "public"."add_numbers"($1::"int4", $2::"int4") AS v) AS __fn
-- args: [2,40]
```

The call is materialized in a subquery so the value expression (which may
subscript arrays or stringify `int8` — `to_jsonb((__fn.v)::text)`) evaluates
it once. Statement shape `scalar`: exec writes the value verbatim.

Void (`noop()` is a golden-only function; the fixture has no void function):

```sql
SELECT 'null'::jsonb AS data FROM (SELECT "public"."noop"()) AS __v
```

→ shape `void`, `204 No Content`.

Single row (table type), with `select=`:

```http
POST /api/rpc/publish_post?select=title,users(email)
{"post_id": 7}
```

```sql
SELECT CASE WHEN t0 IS NULL THEN NULL ELSE jsonb_build_object('title', t0."title", 'users', l2.data) END AS data
FROM "public"."publish_post"($1::"int4") AS t0
LEFT JOIN LATERAL (
  SELECT jsonb_build_object('email', t1."email") AS data
  FROM "public"."users" t1
  WHERE t1."id" = t0."author_id"
  LIMIT 1
) l2 ON true
-- args: [7]
```

A strict function returning NULL yields one all-null row; the `CASE` surfaces
that as JSON `null` (body `null`, `200`) rather than an object of nulls or a
`404` (shape `scalar`). Filters given on the request apply as a `WHERE` on
the function row.

`SETOF <table>` with filters/order/paging/count:

```http
GET /api/rpc/search_posts?term=rest&author_id=eq.1&order=id.desc&limit=20&count=true&select=id,title,users(email)
```

```sql
SELECT jsonb_build_object('rows', coalesce(jsonb_agg(s3.data ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $4), '[]'::jsonb), 'has_next', coalesce(bool_or(s3.__rn > $4), false), 'has_prev', false, 'total', (SELECT count(*) FROM "public"."search_posts"($1::"text") c4 WHERE c4."author_id" = $5)) AS data
FROM (
  SELECT jsonb_build_object('id', t0."id", 'title', t0."title", 'users', l2.data) AS data, row_number() OVER (ORDER BY t0."id" DESC) AS __rn
  FROM "public"."search_posts"($1::"text") t0
  LEFT JOIN LATERAL (
    SELECT jsonb_build_object('email', t1."email") AS data
    FROM "public"."users" t1
    WHERE t1."id" = t0."author_id"
    LIMIT 1
  ) l2 ON true
  WHERE t0."author_id" = $2
  ORDER BY t0."id" DESC
  LIMIT $3
) s3
-- args: ["rest",1,21,20,1]
```

This is exactly the table list statement (`page` shape, `limit+1` scan,
`{'rows','has_next','has_prev'[,'total']}` wrapper — see
[architecture.md](architecture.md)) with the root row source replaced by the
function call and cursors switched off; embeds and `!inner` restrictions
still scan the real tables. exec unwraps it into the body, `X-Total-Count`
and offset `Link`s. Function args are bound once and reused by the count
subquery (filter parameters repeat, as on table lists). `after`/`before` →
`400 keyset_unsupported`. (`published` is not filterable under the default
`filters.indexed_only`; add it to `filters.allow_columns` to filter on it.)

`SETOF <scalar>` (`limit` defaults to `server.max_page_size`):

```http
GET /api/rpc/mood_moods?limit=5&offset=1
```

```sql
SELECT coalesce(jsonb_agg(to_jsonb(__s.v)), '[]'::jsonb) AS data
FROM (SELECT __v.v FROM "public"."mood_moods"() AS __v(v) LIMIT $1 OFFSET $2) AS __s
-- args: [5,1]
```

Composite (non-table) return: `SELECT <composite expansion of __fn.v> AS data
FROM (SELECT fn(...) AS v) AS __fn` — attributes are expanded with the same
`CASE WHEN (v)::text IS NULL THEN NULL ELSE jsonb_build_object(...) END` the
composite columns use, so nested `int8`/`numeric` attributes are strings
(settled, pinned by `TestRPCTypedArguments`). `SETOF <composite>` wraps
`SELECT __v AS v FROM fn(...) AS __v LIMIT/OFFSET` the same way. The one
exception is the `rls.auth.jwt_type` composite: it is emitted raw
(`to_jsonb(__fn.v)`, shapes `jwt` / `jwt_set`) so the claims keep their JSON
numbers (`exp` must be numeric for verification), and exec signs them.

Volatile functions use exactly the same shapes; only the transaction policy
differs.

## Volatile functions are mutations

- `POST` only; `GET` → `405 Allow: POST` (`HEAD` likewise).
- Wrapped in a transaction under `transactions.mutations` (default), with
  `transactions.isolation` and `transactions.max_retries` on `40001`/`40P01`
  (whole request re-run — the function must be safe to re-execute after a
  rolled-back attempt, which it is by definition since nothing committed).
- `server.read_only` → `405 read_only`, `Allow: GET, HEAD` for stable
  functions on the same path shape, `Allow` empty for volatile ones.

  Settled: `405 read_only` with no `Allow` header (an empty `Allow` is not
  emitted) — consistent with tables.

- `RequestHook`s see `Operation{RPC: fn, Mutation: true}`.
- Stable/immutable functions called via `POST` are **not** treated as
  mutations (no tx unless RLS claims).

## Exclusion rules

The first three rows are applied by `pdbcore/introspect` (the catalog never
contains them), the rest by `internal/api`; excluded functions get neither an
RPC route nor a computed column, with a startup log line at `debug` (`warn`
where noted):

| Rule | Reason |
|---|---|
| no `EXECUTE` privilege for the connecting role | can't be called |
| owned by an extension (`pg_depend` on `pg_extension`) | PostGIS alone has thousands |
| any unnamed argument | args are mapped by name |
| any `OUT` / `INOUT` argument (`RETURNS record` / `RETURNS TABLE`) | v1.x — excluded via the `record` return type; a `VARIADIC` array argument is not distinguishable in the catalog and is called as a plain array |
| returns or takes `trigger`, `event_trigger`, `internal`, `language_handler`, `record` (anonymous), `cstring`, polymorphic (`anyelement`, `anyarray`, `anycompatible…`, …) | not callable / not typeable |
| aggregate / window / procedure (`prokind` ≠ `f`) | not a plain function (pdbcore) |
| `@omit` smart comment on the function | user opt-out (`smart-comments` plugin, M5) |
| overloads (same schema + name, different signature) | keep-first by `oid`, warn — later overloads are unreachable |
| function returns a table that is not an exposed resource (`SETOF`/row) | no resource to compile the row against → excluded with warning |
| row-type argument (a table in `schema.schemas`) | first position → [computed column](#computed-columns) instead of RPC; other positions → excluded |

## Computed columns

A `stable`/`immutable` function whose **first argument** is a table's row
type (`users`) becomes a selectable field of that table:

```sql
CREATE FUNCTION users_post_count(u users) RETURNS bigint LANGUAGE sql STABLE AS $$
  SELECT count(*) FROM posts WHERE author_id = u.id $$;
```

| Aspect | Rule |
|---|---|
| key | function name minus the `<table>_` prefix: `users_post_count` → `post_count`; `@name` overrides; a name matching a real column → real column wins, computed skipped with a warning |
| selection | only via `select=` (never in `*`): `GET /api/users?select=id,post_count` |
| SQL | `"public"."users_post_count"(ROW(__t_1."id", __t_1."email", …)::"public"."users")` — an explicit `ROW(...)::table` cast rather than the bare alias, so the same expression works over any row source (`__mut` CTE in mutation responses, `__vis_n` nested visibility CTEs, function row sources) |
| set-returning (`SETOF <scalar>` / `SETOF <table>`) | array field; `SETOF <table>` accepts a nested `select=` like an embed: `select=id,recent_posts(id,title)` |
| `int8` result (`count(*)`) | serialized as string like `int8` columns |
| volatile / `void` / extra unnamed args | skipped |
| filter / order (`post_count=gt.3`, `order=post_count.desc`) | `advanced-filters` plugin (bypasses the index policy by nature; `@omit filter|order` respected) |
| RPC route | none — a function whose first arg is a row type is a computed column, not an RPC (pdbq parity); a row-type arg in any other position excludes the function entirely |
| RETURNING (mutations) | selecting a computed column forces every column of the table into `RETURNING` ([mutations.md](mutations.md#rls-caveats-for-returning)) |
| OpenAPI | listed as a `readOnly` property of the resource schema, absent from `.insert`/`.update` |

```http
GET /api/users/1?select=id,post_count
```

```sql
SELECT jsonb_build_object('etag', md5(to_jsonb(t0)::text), 'row', jsonb_build_object('id', t0."id", 'post_count', ("public"."users_post_count"(ROW(t0."id", t0."email", t0."full_name", t0."mood", t0."settings", t0."tags", t0."balance", t0."created_at", t0."address", t0."prev_addresses", t0."moods")::"public"."users"))::text)) AS data
FROM "public"."users" t0
WHERE t0."id" = $1
LIMIT 1
-- args: [1]
```

(the `etag` wrapper is the single-row ETag convention; goldens
`computed_field`, `computed_set`, `computed_on_mutation` pin the field, the
`SETOF` array and the `__mut` mutation-response cases.)

**Extra arguments are deferred to v1.x.** Functions with additional named
scalar args after the row arg (`posts_excerpt(p posts, len int4)`) are
skipped as computed columns in v1 (and are not RPCs either, because of the
row arg). The `select=` grammar reserves `field(k=v,…)` for them:
`select=id,excerpt(len=80)`; in v1 the parenthesised form is only valid for
PostGIS projection args (`geom(transform=4326)`), and using it on a computed
column → `400`.

## `schema.functions: false`

Removes every `/rpc/*` route and every computed column; the OpenAPI document
has no `rpc` paths and no computed properties; `advanced-filters` computed
filters/order disappear with them. `pdbr plugins list` is unaffected.

## OpenAPI

Each RPC route is a path item under `/rpc/{fn}` (or `/{schema}/rpc/{fn}`):
`get` for stable/immutable (args as query params, plus the `SETOF <table>`
list params), `post` for all (request body `object` with one property per
argument, all optional, typed by the column type map). Responses reference
the returned resource schema (`users`, array of `users`), an inline scalar
schema, `204`, or — for `rls.auth.jwt_type` functions — `string` with
`format: jwt` (array thereof for `SETOF`). Tags per schema. Argument
parameters / request bodies land with the M6 OpenAPI pass. See
[openapi.md](openapi.md).
