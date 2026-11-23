# Aggregate functions

`select=` takes the aggregate functions of PostgREST v12 —
`count()`, `sum()`, `avg()`, `max()`, `min()` — and groups the result by the
other selected items, with no explicit `GROUP BY`. pdbr adds four spatial
aggregates over geometry and geography columns (`extent()`, `union()`,
`collect()`, `centroid()`). Like everything else, an aggregate request is
**one** SQL statement: the same row-level security policies, grants and
`statement_timeout` apply, and values still travel only as parameters.

Aggregates are **off by default**, as in PostgREST (`db-aggregates-enabled`):
an aggregate reads every row its filters match, and `limit` only bounds the
groups it returns. Turn them on with
[`server.aggregates: true`](configuration.md) (`PDBR_SERVER_AGGREGATES=true`,
`--server.aggregates`); while off, any aggregate in `select=` answers
`400 aggregates_disabled` (PostgREST `PGRST123`).

## Grammar

```
item      := … | [alias ":"] aggregate
aggregate := "count()"
           | column [ "::" type ] "." fn "()" [ "::" type ]
fn        := count | sum | avg | max | min              -- PostgREST
           | extent | union | collect | centroid        -- pdbr, spatial columns
type      := [a-z_][a-z0-9_]*                            -- case-insensitive
```

| Item | Meaning | Response key |
|---|---|---|
| `count()` | `count(*)`: rows of the group | `count` |
| `col.count()` | `count(col)`: non-NULL values of `col` | `count` |
| `col.sum()` / `col.avg()` | `sum(col)` / `avg(col)` over numeric (and `interval`; `sum` also `money`) columns | `sum` / `avg` |
| `col.max()` / `col.min()` | `max(col)` / `min(col)` over any ordered type (numbers, text, dates and timestamps, enums, arrays, …) | `max` / `min` |
| `total:col.sum()` | alias | `total` |
| `col::int.sum()` | input cast: `sum((col)::int)` | `sum` |
| `col.avg()::int` | output cast: `(avg(col))::int` | `avg` |
| `count` (no parentheses) | legacy PostgREST spelling of `count()`, accepted when the resource has no column, computed field or relation named `count` | `count` |

The type of a cast is a single SQL type name (`int`, `bigint`, `numeric`,
`float8`, `text`, `date`, an enum or domain name, …), rendered unquoted as
written, lower-cased. A name PostgreSQL does not know is a database error
(`400 pg:42704`).

Casts are only accepted on aggregates: `select=id::text` is `400
invalid_select` (PostgREST casts any column; see
[Differences from PostgREST](#differences-from-postgrest)).

## Grouping

Every selected item that is not an aggregate is a grouping key:

```http
GET /api/posts?select=count()
GET /api/posts?select=author_id,count(),newest:id.max()&order=author_id.desc
GET /api/users?select=mood,total:balance.sum(),mean:id::float8.avg()
```

```json
[{"count": 4}]
[{"author_id": 3, "count": 1, "newest": 4}, {"author_id": 2, "count": 1, "newest": 3}, {"author_id": 1, "count": 2, "newest": 2}]
[{"mood": "ok", "total": 0, "mean": 2}, {"mood": "happy", "total": 0, "mean": 2}]
```

- Grouping keys may be columns (with spatial projection arguments),
  computed columns, PostGIS pseudo-columns and embeds; `*` groups by every
  column.
- Without any grouping key the response is one row, also when no row
  matches (`[{"count": 0}]`, `[{"sum": null}]`); with keys and no matching
  row it is `[]`.
- Filters (`col=op.value`, `and=`/`or=`, relation filters, `!inner`
  embeds) restrict the rows **before** grouping. There is no `HAVING`
  (PostgREST has none either).
- Two items answering under the same key are `400 invalid_select`:
  `select=id.sum(),balance.sum()` needs an alias on one of them.

### Values

Aggregate results are rendered with PostgreSQL's `to_jsonb`, as PostgREST
returns them: `count`, `sum`, `avg` and numeric `max`/`min` results are JSON
**numbers**, `bigint` and `numeric` included (pdbr renders `int8`, `numeric`
and `money` *columns* as strings, [api.md](api.md#request-and-response-bodies);
cast the result, `col.sum()::text`, for an exact string). `avg` of an integer
column is `numeric` and keeps its scale (`2.0000000000000000`); cast it
(`::float8`, `::int`) for a plain number. Timestamps, enums and text come
back as strings; grouping keys keep the representation of their column.

## Aggregates and embeds

**Grouping by an embed.** An embedded resource is a grouping key like a
column (to-one embeds give one object per group, to-many embeds group by the
whole embedded array):

```http
GET /api/posts?select=id.count(),users(mood)
```

```json
[{"count": 3, "users": {"mood": "happy"}}, {"count": 1, "users": {"mood": "ok"}}]
```

**Aggregates inside an embed.** Aggregates in a to-many embed (backward, or
forward over a `PERIOD` foreign key) are computed per parent row, grouped by
the embed's own other items and independent of the parent's columns:

```http
GET /api/users?select=id,email,posts(count())
GET /api/users?select=id,posts(author_id,n:count(),id.max())&posts.order=author_id.desc&posts.limit=5
```

```json
[{"id": 1, "email": "ada@example.com", "posts": [{"count": 2}]}, …]
```

The embed's own parameters keep their meaning — `<rel>.<col>=` filters the
related rows before grouping, `<rel>.order=` orders the groups (by grouping
columns only), `<rel>.limit=`/`<rel>.offset=` page the groups. Aggregates in
a to-one embed are `400 invalid_select` (see the differences below).

## Ordering and paging

- `order=` accepts only **grouping columns** — selected, non-aggregated
  columns of the same node (with `*`, any column). Ordering by an aggregate,
  a column that is not selected, a computed column or a related column is
  `400 column_not_orderable`. The column's `filters.indexed_only` order policy
  still applies.
- After the requested terms pdbr orders by every remaining grouping key, so
  the order of groups — and with it `limit`/`offset` paging — is total.
- `limit`/`offset` page **groups**: `limit` defaults to and is capped by
  `server.max_page_size`, and `Link` headers carry `offset=` links.
- Keyset cursors do not address groups: `after=`/`before=` with aggregates
  are `400 keyset_unsupported`, and no `X-Next-Cursor`/`X-Prev-Cursor` is
  sent. `distinct=` with aggregates is `400 invalid_select` (the grouping
  keys already make rows distinct).
- `count=true` and `Prefer: count=exact|planned|estimated` total the
  **groups** (`X-Total-Count`, `Content-Range: 0-24/3`); planned and
  estimated totals fall back to the exact count (`Preference-Applied:
  count=exact`), since the table statistic counts rows, not groups.

## Where aggregates apply

| Request | Aggregates |
|---|---|
| `GET`/`HEAD {base}/{table}` | yes |
| `GET`/`HEAD`/`POST {base}/rpc/{fn}` returning `SETOF <table>` | yes, over the function's rows |
| to-many embeds of those | yes |
| `{base}/{table}/{pk}`, `/by/{cols}/{vals}`, RPCs returning one row | `400 invalid_select` (one row, nothing to aggregate) |
| `select=` on `POST`/`PUT`/`PATCH`/`DELETE` responses | `400 invalid_select` |
| to-one embeds, `SETOF <table>` computed columns | `400 invalid_select` |

`Accept: text/csv` works on aggregated lists without embeds; the header row
is the response keys in `select=` order (`author_id,count`).

## Spatial aggregates (pdbr extension)

On geometry and geography columns (plugin `postgis`, setting `aggregates`,
on by default) four more functions return **GeoJSON**:

| Item | SQL | Result |
|---|---|---|
| `col.extent()` | `ST_SetSRID(ST_Extent(col)::geometry, <srid>)` | bounding box of the group as a `Polygon` (a `Point`/`LineString` when degenerate) |
| `col.union()` | `ST_Union(col)` | point-set union (dissolves overlaps) |
| `col.collect()` | `ST_Collect(col)` | `Multi*`/`GeometryCollection` of the group's geometries, unchanged |
| `col.centroid()` | `ST_Centroid(ST_Collect(col))` | centroid of the collected geometries |

```http
GET /api/places?select=owner_id,location.extent(),location.centroid(),n:location.count()
```

```json
[{"owner_id": 1, "n": 2,
  "extent": {"type": "Polygon", "coordinates": [[[-0.1281, 51.508], [-0.1281, 51.5194], [-0.127, 51.5194], [-0.127, 51.508], [-0.1281, 51.508]]]},
  "centroid": {"type": "Point", "coordinates": [-0.12755, 51.5137]}}, …]
```

- Geography input is cast to `geometry` (the PostGIS aggregates are planar):
  extents and centroids are computed in degrees of WGS84.
- `extent()` carries the column's typmod SRID (geography: 4326); over an
  unconstrained geometry column the SRID is unknown and the GeoJSON has no
  `crs` member, like any WGS84 value.
- Input casts are not accepted on spatial aggregates; an output cast
  (`col.extent()::text`) replaces the GeoJSON with the cast value.
- `count()`/`col.count()` work on spatial columns; `sum`/`avg`/`max`/`min`
  do not (`400 invalid_select`, pointing to the spatial functions).
- With the plugin disabled, or `plugins.settings.postgis.aggregates: false`,
  the spatial functions are `400 invalid_select`; the PostgREST functions
  are unaffected.

## Cost

An aggregate reads every row its filters match, so `server.max_cost`
([pagination.md](pagination.md#cost-limit)) costs an aggregated list **as if
it had no limit**: the root at `server.max_page_size`, a to-many embed at 10
(or its `@costMultiplier`); `limit` still bounds the response. Each
aggregate costs 1 like a column. Examples with the defaults:

| Request | Cost |
|---|---|
| `posts?select=author_id,count()&limit=5` | 1 + 100 × 2 = 201 |
| `users?select=id,posts(count())` | 1 + 100 × (1 + 1 + 10 × 1) = 1201 |
| `users?select=id,posts(count())&limit=5` | 1 + 5 × 12 = 61 |

Aggregates are only as fast as the scan beneath them; index the filter and
grouping columns of the aggregates you expose, and keep
`database.statement_timeout` set.

## Errors

| Request | Response |
|---|---|
| any aggregate while `server.aggregates` is off | `400 aggregates_disabled` |
| bad grammar (`col.sum(x)`, an empty or malformed cast, a hint on an aggregate), a cast on a non-aggregate, an aggregate over a computed or derived field, a type without the aggregate (`text.sum()`, `jsonb.max()`, `uuid.max()`), duplicate response keys, aggregates outside list reads or in to-one embeds, `distinct=` | `400 invalid_select` |
| unknown column | `400 unknown_column` |
| `order=` on something other than a grouping column | `400 column_not_orderable` |
| `after=` / `before=` | `400 keyset_unsupported` |
| unknown cast type | `400 pg:42704` |

## Differences from PostgREST

pdbr follows the PostgREST v12 grammar and semantics above; where it
differs:

- **Spatial aggregates** (`extent`, `union`, `collect`, `centroid`) are a
  pdbr extension.
- **Spread embeds** (`...customers(name)`) do not exist in pdbr, so neither
  does grouping by a spread nor hoisting the aggregates of a to-one spread;
  aggregates inside a non-spread to-one embed are rejected rather than
  computed over its single row.
- **Casts** are accepted on aggregates only; PostgREST also casts plain
  columns (`select=id::text`). JSON paths (`col->key::numeric.sum()`) are
  not part of pdbr's `select=` grammar.
- **Validation before the database**: `sum`/`avg` of a non-numeric column,
  `max`/`min` of `json`/`jsonb`/`bool`/`bytea`/`uuid`/composite columns,
  duplicate response keys and ordering by a non-grouping column are `400`
  pdbr codes instead of PostgreSQL errors.
- **Totals count groups**: `count=`/`Prefer: count=` on an aggregated list
  report the number of groups (what `Content-Range` pages over), and the
  planned/estimated modes fall back to exact.
- **Deterministic group order**: groups are ordered by the remaining
  grouping keys after `order=`, so offset paging is stable.
- **Keyset paging** is pdbr's; with aggregates it is unavailable
  (`400 keyset_unsupported`), as is `distinct=`.
- **Errors** use pdbr's problem codes: `aggregates_disabled` stands for
  `PGRST123`.
