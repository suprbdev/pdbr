# RPC (functions) and computed columns

As built (M4). Every function in `schema.schemas` that
passes the [exclusion rules](#exclusion-rules) becomes an RPC route; functions
whose first argument is a table's row type instead become
[computed columns](#computed-columns) (stable/immutable ones; a volatile one
is an RPC taking the row, see [row arguments](#table-valued-row-arguments)). `schema.functions: false` disables
both. One request → one statement → one json `data` row, as everywhere else.

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
  inflection) and passed in named notation (`"f"("a" => $1::"int4")`,
  `pdbcore/introspect.Function.CallSQL`), PostgREST semantics:
  - an **omitted** argument is left out of the call, so PostgreSQL applies
    its `DEFAULT`; omitting an argument that has no default →
    `400 missing_argument` (detail names every missing argument);
  - an explicit JSON `null` (POST) is passed as `NULL::<type>`;
  - a `VARIADIC` argument takes one element per repetition of its query
    parameter on GET (`?nums=1&nums=2`) or a JSON array in the body
    (`{"nums": [1, 2]}`, `[]` for zero elements), and is passed as
    `VARIADIC $n::<type>[]`. PostgreSQL matches named notation to a variadic
    function only when every argument is passed, so a variadic function is
    called positionally: only trailing defaulted arguments can be omitted
    (an omitted argument before a passed one is `missing_argument` too).
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
  JSON-`null` argument is the literal `NULL::<type>`). The SQL text depends
  on which arguments are present (and null), never on their values:

| PG type | GET (`?arg=`) | POST body |
|---|---|---|
| numeric / bool / text-like | text, cast by PG | JSON scalar (`int8`/`numeric` number **or** string) |
| enum | label verbatim | label verbatim |
| array | `{a,b}` PG literal | JSON array |
| composite | JSON text | JSON object → `jsonb_populate_record(NULL::type, $n::jsonb)`; composite arrays → one jsonb array parameter populated element-wise (`jsonb_array_elements` + `jsonb_populate_record`, `null` elements kept, order kept) |
| table row type / array of rows | JSON text | JSON object (array of objects and `null`s) keyed by the table's readable columns — see [row arguments](#table-valued-row-arguments) |
| `json`/`jsonb` | JSON text | any JSON value |
| geometry/geography (postgis plugin) | GeoJSON or WKT/EWKT text | GeoJSON object or WKT/EWKT string → one `CASE WHEN left($n, 1) = '{' THEN ST_GeomFromGeoJSON($n) ELSE ST_GeomFromEWKT($n) END` whatever the input form |
| any other | text `::type` | string `::type` |

- Polymorphic arguments (`anyelement`, `anyarray`, `anyrange`, the
  `anycompatible…` family) take the concrete type the function's
  `@anyelement <type>` / `@anycompatible <type>` smart tag names (see
  [polymorphic functions](#polymorphic-functions)); after resolution they
  are coerced and cast like any argument of that type.
- Uncoercible value → `400 invalid_body` (POST) / `400 invalid_filter` (GET)
  when detectable at compile time; otherwise PG's `22*` → `400 pg:22P02`.
- Body must be a JSON object (`{}` allowed → every argument omitted);
  non-object → `400 invalid_body`; missing body on POST is treated as `{}`.

## Return shapes

| Function returns | Response body | Status | Extras |
|---|---|---|---|
| scalar (`int4`, `text`, `jsonb`, array, enum, …) | the JSON value (`42`, `"x"`, `[1,2]`, `null`) | `200` | `int8`/`numeric` as strings |
| `void` | none | `204` | |
| single row: table type or composite | object; `null` when the function returns NULL | `200` | `select=` (embeds for table types) |
| `SETOF <table>` | array of objects | `200` | `select=`, filters, `order=`, `limit`/`offset`, `count=true` → `X-Total-Count`, `Accept: text/csv`; **offset paging only** (no `after`/`before`, no cursors, `Link` with `offset=`) |
| `SETOF <scalar>` / `SETOF <composite>` | array of values / objects | `200` | `limit`/`offset` (`limit` defaults to and is capped by `server.max_page_size`; no `count`) |
| `rls.auth.jwt_type` composite (`SETOF` too) | signed HS256 JWT string (array of strings) | `200` | minted by `pdbcore/jwtmint` from the row's fields as claims |
| `RETURNS TABLE(...)` / `SETOF record` with `OUT` params (two or more result columns) | array of objects, one key per result column | `200` | as `SETOF <table>`: `select=` (columns and aliases; no embeds), filters and `order=` on every result column that holds no composite, table row or raster (no index policy: the output is scanned), `limit`/`offset`, `count=true`, `Accept: text/csv`; offset paging only; a `raster` result column is left out of the rows |
| `OUT` / `INOUT` params, not `SETOF` (two or more) | one object, one key per output parameter (all-null columns stay an object) | `200` | `select=` |
| one output parameter / `RETURNS TABLE(x …)` with one column | PostgreSQL returns that column's type, so the function is a scalar / `SETOF <scalar>` | `200` | |
| anonymous `record` (no output parameters) | | | excluded (no shape) |

**Nested rows.** A table row nested inside a value (an array of rows such
as `RETURNS docs[]`, a `RETURNS TABLE` column of row type, a composite
attribute of row type, a domain over a row type, a computed column
returning the row) renders as the object `select=*` gives for that
table's resource: its readable columns under their keys, recursively, so a
column hidden by `@omit read` or a [contract](contracts.md) and raster
columns never appear. Composite attributes of type `raster` are left out
the same way, and a function returning `raster` (or `raster[]`) is not
exposed. OpenAPI references the resource's read schema for such a value.

`SETOF <table>` results honour the table's filter/order policy
(`filters.indexed_only` etc.) — the function output is scanned, so the index
policy is advisory here, but the same rules keep the surface predictable.
`limit` defaults to and is capped by `server.max_page_size`.

Settled: `SETOF <scalar>` / `SETOF <composite>` get `limit`/`offset` but no
`count=true` (ignored).

Record results (`pdbcore/introspect.Function.ReturnColumns`) are compiled
against a route-less resource named `rpc.<fn path>` whose columns are the
output parameters, so `RETURNS TABLE` functions get exactly the `SETOF
<table>` statement over `FROM "public"."mood_counts"() t0`, and OpenAPI
documents the rows as the `rpc.<fn path>` component schema:

```http
GET /api/rpc/mood_counts?select=mood,n&n=gt.0&order=n.desc,mood&limit=5&count=true
```

(`mood_counts() RETURNS TABLE(mood mood, n bigint)`, golden
`rpc_record_setof`; `user_stats(uid int4, OUT posts bigint, OUT last_title
text)` is `rpc_record_row`.)

## SQL shapes

Scalar:

```http
POST /api/rpc/add_numbers
{"a": 2, "b": 40}
```

```sql
SELECT to_json(__fn.v) AS data
FROM (SELECT "public"."add_numbers"("a" => $1::"int4", "b" => $2::"int4") AS v) AS __fn
-- args: [2,40]
```

The call is materialized in a subquery so the value expression (which may
subscript arrays or stringify `int8` — `to_json((__fn.v)::text)`) evaluates
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
SELECT CASE WHEN t0 IS NULL THEN NULL ELSE json_build_object('title', t0."title", 'users', l2.data) END AS data
FROM "public"."publish_post"($1::"int4") AS t0
LEFT JOIN LATERAL (
  SELECT json_build_object('email', t1."email") AS data
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
SELECT json_build_object('has_next', coalesce(bool_or(s3.__rn > $4), false), 'has_prev', false, 'total', (SELECT count(*) FROM "public"."search_posts"("term" => $1::"text") c4 WHERE c4."author_id" = $5), 'n', count(*) FILTER (WHERE s3.__rn <= $4), 'rows', coalesce(json_agg(s3.data ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'title', t0."title", 'users', l2.data) AS data, row_number() OVER (ORDER BY t0."id" DESC) AS __rn
  FROM "public"."search_posts"("term" => $1::"text") t0
  LEFT JOIN LATERAL (
    SELECT json_build_object('email', t1."email") AS data
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
`{'has_next','has_prev'[,'total'],'n','rows'}` wrapper — see
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
SELECT coalesce(json_agg(to_json(__s.v)), '[]'::json) AS data
FROM (SELECT __v.v FROM "public"."mood_moods"() AS __v(v) LIMIT $1 OFFSET $2) AS __s
-- args: [5,1]
```

Composite (non-table) return: `SELECT <composite expansion of __fn.v> AS data
FROM (SELECT fn(...) AS v) AS __fn` — attributes are expanded with the same
`CASE WHEN (v)::text IS NULL THEN NULL ELSE json_build_object(...) END` the
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

## Table-valued (row) arguments

An argument typed as a table's row type (`u users`) or an array of rows
(`ps posts[]`) takes the row as a JSON object, PostgREST-style:

```
POST /rpc/user_greeting   {"greeting": "Hello", "u": {"full_name": "Ada", "email": "ada@example.com"}}
POST /rpc/total_title_length   {"ps": [{"title": "abc"}, null, {"title": "de"}]}
GET  /rpc/user_greeting?greeting=Hi&u=%7B%22email%22%3A%22a%40b%22%7D     (the object as JSON text)
POST /rpc/rename_user   {"u": {"id": 2}, "new_name": "Grace"}            (volatile: POST only)
```

- **Keys** are the table's readable columns: every column the resource
  exposes for reading, including the primary key and generated/identity
  columns; `@omit read` columns are hidden here too. `@omit create` /
  `@omit update` and column INSERT/UPDATE privileges do not apply: the
  argument is a value handed to the function, not a write. Every key is
  optional; an omitted key is a `NULL` attribute. An unknown or hidden key,
  a non-object value or an uncoercible attribute → `400 invalid_body`
  (POST) / `400 invalid_filter` (GET), naming the argument and column.
- **Values** follow the request-body coercion of each column (enum labels,
  `int8`/`numeric` as number or string, arrays, json, composites, GeoJSON or
  WKT/EWKT for spatial columns); hstore, range and multirange columns take
  their literal string form only (`"[1,5)"`) inside a row argument.
- **GET** passes the object (array of objects) as JSON text in the query
  parameter; a `VARIADIC` row argument takes one JSON object per
  repetition.
- **SQL**: the whole row is one jsonb parameter,
  `jsonb_populate_record(NULL::"public"."users", $n::jsonb)`; an array is
  populated element-wise like composite arrays (`null` elements stay
  `NULL`). A JSON `null` argument is `NULL::"public"."users"`.
- **Exposure**: a function taking rows of a table that is not an exposed
  resource is skipped (`info` log line), and removing a resource (an
  `APIHook`) removes the RPCs taking its rows. A stable/immutable function
  whose **first** argument is a row stays a [computed column](#computed-columns)
  only; a volatile one is an RPC (POST) taking the row.
- OpenAPI: the argument references the `<resource>.row` component (array
  `items` for arrays): an object with every readable column, all optional
  and nullable, `additionalProperties: false`. It is generated only for
  tables some function takes. GET parameters are `string` (JSON text).

## Polymorphic functions

PostgreSQL resolves a polymorphic signature from the actual types of the
arguments in a call (a non-polymorphic sibling argument does not take part),
so the API needs to be told the type. Tag the function:

```sql
COMMENT ON FUNCTION pick_first(anyelement, anyelement) IS '@anyelement text';
COMMENT ON FUNCTION larger_of(anycompatible, anycompatible) IS '@anycompatible numeric';
COMMENT ON FUNCTION range_lower(anyrange) IS '@anyelement integer';   -- r: int4range, returns int4
```

The `smart-comments` plugin (pdbcore's catalog hook) rewrites the signature:
`anyelement`/`anynonarray`/`anyenum` become the named type, `anyarray` its
array, `anyrange`/`anymultirange` the range over it (the built-in range, or
the single range type with that subtype) and its multirange; the
`anycompatible…` family likewise from `@anycompatible`. The return type and
`OUT` columns resolve too. Arguments are then cast to the concrete types
(`"a" => $1::"text"`), which makes PostgreSQL resolve the call the same way.
The type is any SQL spelling (`integer`, `timestamp with time zone`,
`numeric(10,2)`, `text[]`, `public.mood`, or an unqualified enum,
composite, table, domain or range of the introspected schemas). A function
that stays polymorphic — no tag, an unusable one (logged as a warning), or
`smart-comments` disabled — is skipped with one `info` log line naming the
tags.

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
| returns or takes `trigger`, `event_trigger`, `internal`, `language_handler`, `record` (anonymous), `cstring` | not callable / not typeable |
| polymorphic (`anyelement`, `anyarray`, `anycompatible…`, …) without a resolving `@anyelement` / `@anycompatible` tag | no concrete type to cast to (`info` log line); see [polymorphic functions](#polymorphic-functions) |
| aggregate / window / procedure (`prokind` ≠ `f`) | not a plain function (pdbcore) |
| `@omit` smart comment on the function | user opt-out (`smart-comments` plugin, M5) |
| overloads (same schema + name, different signature) | keep-first, warn — later overloads are unreachable; the first is the one with the fewest input parameters, ties broken by parameter types (type schema, then name), the same on every run and database |
| function returns a table that is not an exposed resource (`SETOF`/row) | no resource to compile the row against → excluded with warning |
| row-type argument (or array of rows) of a table that is not an exposed resource | no row shape to accept (`info` log line) |
| result, argument or `OUT`/`TABLE` column whose type is, or holds through a domain, a composite attribute or an array (recursively), the row of a table hidden by a bare `@omit` or a [schema contract](contracts.md), including through a resolved `@anyelement` / `@anycompatible` tag; or holds, through a domain or composite, the row of a table that is not an exposed resource | the row would be rendered whole as JSON, hidden columns included (`info` log line); table columns of such a type are hidden the same way, and the composite gets no OpenAPI schema |
| result, argument or `OUT`/`TABLE` column whose type is, or holds as above, a type in a schema outside `schema.schemas` that is no known enum, composite, domain, range, spatial or extension type (typically the row of a table in an unexposed schema) | same reason, excluded with warning; table columns of such a type are hidden with a warning |
| returns `raster` or `raster[]` (postgis_raster) | raster bytes are never returned ([postgis.md](postgis.md#rasters)); encode them in a computed column instead |
| row-type argument in first position, stable/immutable | [computed column](#computed-columns) instead of RPC; volatile → RPC with a [row argument](#table-valued-row-arguments) |

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
| SQL | `"public"."users_post_count"("u" => ROW(__t_1."id", __t_1."email", …)::"public"."users")` — an explicit `ROW(...)::table` cast rather than the bare alias, so the same expression works over any row source (`__mut` CTE in mutation responses, `__vis` / `__vis_n` visibility CTEs, function row sources) |
| set-returning (`SETOF <scalar>` / `SETOF <table>`) | array field; `SETOF <table>` accepts a nested `select=` like an embed: `select=id,recent_posts(id,title)` |
| `int8` result (`count(*)`) | serialized as string like `int8` columns |
| volatile | RPC (POST) with a [row argument](#table-valued-row-arguments), not a computed column |
| `void` | skipped |
| extra arguments after the row | passed as `select=field(k=v,…)` (see below); omitted ones take their `DEFAULT`, an omitted one without a default → `400 missing_argument`; `VARIADIC`, `INOUT` and row-typed extras → the function is skipped |
| filter / order (`post_count=gt.3`, `order=post_count.desc`) | `advanced-filters` plugin, only for columns whose extra arguments all have defaults (called with the defaults) (bypasses the index policy by nature; `@omit filter|order` respected) |
| RPC route | none — a stable/immutable function whose first arg is a row type is a computed column, not an RPC (pdbq parity); a row-type arg in any other position makes an RPC with a [row argument](#table-valued-row-arguments) |
| RETURNING (mutations) | selecting a computed column forces every column of the table into `RETURNING` ([mutations.md](mutations.md#rls-caveats-for-returning)) |
| OpenAPI | listed as a `readOnly` property of the resource schema, absent from `.insert`/`.update` |

```http
GET /api/users/1?select=id,post_count
```

```sql
SELECT json_build_object('etag', md5(to_jsonb(ROW(t0."id", t0."email", t0."full_name", t0."mood", t0."settings", t0."tags", t0."balance", t0."created_at", t0."address", t0."prev_addresses", t0."moods"))::text), 'row', json_build_object('id', t0."id", 'post_count', ("public"."users_post_count"("u" => ROW(t0."id", t0."email", t0."full_name", t0."mood", t0."settings", t0."tags", t0."balance", t0."created_at", t0."address", t0."prev_addresses", t0."moods")::"public"."users"))::text)) AS data
FROM "public"."users" t0
WHERE t0."id" = $1
LIMIT 1
-- args: [1]
```

(the `etag` wrapper is the single-row ETag convention; goldens
`computed_field`, `computed_set`, `computed_on_mutation` pin the field, the
`SETOF` array and the `__mut` mutation-response cases.)

**Extra arguments** go in the select item: `posts_excerpt(p posts, max_chars
int4)` is `select=id,excerpt(max_chars=80)`. Each `k=v` is validated against the
function's arguments (unknown → `400 unknown_argument`), coerced by the
argument's pg type (`400 invalid_filter` on a bad value) and bound as a typed
parameter; the call uses named notation and passes only the arguments present,
so the SQL text depends on which arguments are given, never on their values
(golden `computed_args`). A required argument that is absent → `400
missing_argument` (also for a bare `select=excerpt`). Values may be
double-quoted to carry `,` `)` or `=`. A `SETOF <table>` column takes `k=v`
items and its nested select in the same parentheses:
`recent_posts(n=2,id,title)`. The OpenAPI property description of each
computed column lists its arguments with type, required/optional and default.

## `schema.functions: false`

Removes every `/rpc/*` route and every computed column; the OpenAPI document
has no `rpc` paths and no computed properties; `advanced-filters` computed
filters/order disappear with them. `pdbr plugins list` is unaffected.

## OpenAPI

Each RPC route is a path item under `/rpc/{fn}` (or `/{schema}/rpc/{fn}`):
`get` for stable/immutable (args as query params, plus the `SETOF <table>` /
`SETOF record` list params), `post` for all (request body `object` with one property per
argument, typed by the column type map). Arguments without a `DEFAULT` are
`required` (query parameter `required: true`, body `required` list, body
`required: true` when there is at least one); defaulted ones are optional and
their description shows the default expression (`optional, default
'hello'::text.`). A `VARIADIC` argument is an `array` query parameter
(`style: form`, `explode: true`) / a JSON array property. A row-typed
argument references the `<resource>.row` component. Responses reference
the returned resource schema (`users`, array of `users`), the
`rpc.<fn path>` schema of a record result (one property per output
parameter; non-nullable for a single record), an inline scalar
schema, `204`, or — for `rls.auth.jwt_type` functions — `string` with
`format: jwt` (array thereof for `SETOF`). Tags per schema. Argument
parameters / request bodies land with the M6 OpenAPI pass. See
[openapi.md](openapi.md).
