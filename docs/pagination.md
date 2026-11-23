# Pagination, ordering, counting

Every list (`GET {base}/{table}`, `SETOF` RPCs, backward embeds) is bounded:
there is no unbounded scan. Paging is either **offset** (`limit`/`offset`) or
**keyset** (`after`/`before` cursors anchored on the primary key); both
compile into the single statement, and both are driven by `order=`.

## `limit` and `offset`

| Param | Default | Notes |
|---|---|---|
| `limit` | `server.max_page_size` (100) | larger than the cap → `400 page_size_exceeded`; `0` or negative → `400` |
| `offset` | `0` | negative → `400`; cannot combine with `before` |

```http
GET /api/posts?limit=20&offset=40
```

Absent `limit` means the cap, not "everything". `offset` is the only paging
mode for views and other PK-less resources, for `distinct=`, and for `SETOF`
RPC results.

## Keyset cursors: `after` / `before`

A cursor is base64url (no padding) of the JSON array
`["schema.table", pk…]` — the anchor row's primary key, one element per PK
column:

```
["public.posts", 7]        → WyJwdWJsaWMucG9zdHMiLDdd
["public.memberships", 7, "eng"]
```

Cursors are decoded structurally, not trusted as SQL: the type name must match
the requested resource, the arity must match the PK, and each element is
coerced to its PK column type before it becomes a bind parameter. Anything
else is `400 invalid_cursor`.

- `after=<cursor>` returns the page that follows the anchor row under the
  requested `order=`; the anchor itself is excluded.
- `before=<cursor>` returns the page immediately *preceding* the anchor: the
  scan runs in reversed order with the same `limit` and the result is
  re-reversed, so the body is always in the requested order.
- `offset` may combine with `after` (skips rows past the anchor) but not with
  `before` → `400 keyset_unsupported`.
- Requires a primary key. Views and PK-less tables → `400 keyset_unsupported`
  unless a `@primaryKey` smart comment gives the view one.
- Cursors are index-friendly: they compile to lexicographic predicates on
  the `ORDER BY` columns plus the PK tiebreaker (`(title, id) > ($1, $2)` for a
  uniform direction, expanded comparisons for mixed directions or nullable
  terms), never to `OFFSET`. The expanded form is a top-level `OR` that no
  index can bound, so when the first order term is a `NOT NULL` column it is
  preceded by the redundant bound `title >= $1` (`<=` for a descending scan,
  the extension's qualified operator for citext/ltree/hstore): the index on
  that column then starts the scan at the anchor. A nullable first term gets
  no bound, since one that also admits NULLs is again an `OR`; such orders
  still scan from the start of the index up to the anchor. The anchor's
  non-PK order values are read by a correlated subquery on the PK inside the
  same statement.
- A cursor stays decodable if `order=` changes; the page is then relative to
  the anchor row under the *new* order.
- If the anchor row has been deleted: an ordering entirely on primary-key
  columns still pages on (the cursor carries the PK values, which become
  plain parameters — the pdbq refinement, as built in `compile.anchorVals`);
  an ordering that involves a non-PK column comes back empty, because the
  anchor's order values are read by a scalar subquery on the PK that now
  yields NULL.

Cursors are not interchangeable with pdbq `nodeId`s: the first element is
`schema.table`, not the GraphQL type name.

## Response headers and `Link`

| Header | Present when | Value |
|---|---|---|
| `X-Next-Cursor` | keyset-capable resource and a next page exists | cursor of the last row in the body |
| `X-Prev-Cursor` | keyset-capable resource and a previous page exists | cursor of the first row in the body |
| `Link` | any list | `<url>; rel="next"`, `<url>; rel="prev"` — the request URL with `after`/`before` (or `offset`) replaced; see [Link URLs](#link-urls) |
| `X-Total-Count` | `count=true` or `Prefer: count=` | total matching rows, ignoring `limit`/`offset`/cursors (an estimate under `count=planned`/`estimated`) |
| `Content-Range` | every offset-addressed list page (no `after`/`before`) | `<first>-<last>/<total>` by row position, `*` for an empty page or when no count was requested: `0-24/*`, `25-49/1043`, `*/1043`; see [`Prefer: count=`](#prefer-count-and-content-range) |

Whether a further page exists in the direction being paged is exact (one
extra row is fetched and dropped). The opposite direction is reported as
existing whenever a cursor was supplied — the anchor row is there by
definition — and omitted on a cursor-less first page.

Decided (M6): the rule stays as built — the opposite side reflects the
supplied cursor (a `prev` link can point at an empty page when the anchor
row was deleted between requests; the client simply gets `[]`).

Offset-only resources get `Link` with `offset=` and never emit cursors:
`rel="next"` when the page was full, `rel="prev"` when `offset > 0`.

### Link URLs

`Link` URLs are **relative** (`{server.base_path}{path}?{query}`, no scheme
or host, so they work behind any proxy) and **URL-preserving**
(`internal/exec.withParam`): the request's raw query string is kept verbatim
and in order — `select=`, filters, `order=`, `count=`, percent-encoding and
all — and only the paging parameters change:

- `rel="next"` on a keyset page: `after=<X-Next-Cursor>` replaces the
  request's `after=` in place (appended when absent); `before=` and
  `offset=` are dropped (the cursor is an exact continuation).
- `rel="prev"` on a keyset page: `before=<X-Prev-Cursor>` likewise; `after=`
  and `offset=` are dropped.
- Offset-only lists (and `SETOF <table>` functions): `offset=` is replaced in
  place / appended with `offset ± limit` (never below 0); nothing is dropped.
- Duplicate occurrences of the rewritten parameter are collapsed to one.

`HEAD` carries exactly the same `Link`, `X-Total-Count`, `Content-Range` and
cursor headers as `GET`, and so do `text/csv` responses.

### Walk-through

Page 1:

```console
$ curl -si 'localhost:8080/api/posts?order=title.asc&limit=2&count=true'
HTTP/1.1 200 OK
X-Total-Count: 5
X-Next-Cursor: WyJwdWJsaWMucG9zdHMiLDJd
Link: </api/posts?order=title.asc&limit=2&count=true&after=WyJwdWJsaWMucG9zdHMiLDJd>; rel="next"

[{"id":7,"title":"Hello","author_id":1,"published":true},
 {"id":2,"title":"Intro","author_id":1,"published":false}]
```

Page 2, following `rel="next"`:

```console
$ curl -si 'localhost:8080/api/posts?order=title.asc&limit=2&count=true&after=WyJwdWJsaWMucG9zdHMiLDJd'
HTTP/1.1 200 OK
X-Total-Count: 5
X-Next-Cursor: WyJwdWJsaWMucG9zdHMiLDIxXQ
X-Prev-Cursor: WyJwdWJsaWMucG9zdHMiLDEzXQ
Link: </api/posts?order=title.asc&limit=2&count=true&after=WyJwdWJsaWMucG9zdHMiLDIxXQ>; rel="next", </api/posts?order=title.asc&limit=2&count=true&before=WyJwdWJsaWMucG9zdHMiLDEzXQ>; rel="prev"

[{"id":13,"title":"Notes","author_id":2,"published":true},
 {"id":21,"title":"Plans","author_id":2,"published":true}]
```

Back to page 1 via `rel="prev"` — same order, same rows as the first call:

```console
$ curl -si 'localhost:8080/api/posts?order=title.asc&limit=2&count=true&before=WyJwdWJsaWMucG9zdHMiLDEzXQ'
HTTP/1.1 200 OK
X-Total-Count: 5
X-Next-Cursor: WyJwdWJsaWMucG9zdHMiLDJd
Link: </api/posts?order=title.asc&limit=2&count=true&after=WyJwdWJsaWMucG9zdHMiLDJd>; rel="next"

[{"id":7,"title":"Hello","author_id":1,"published":true},
 {"id":2,"title":"Intro","author_id":1,"published":false}]
```

The `before` request compiles to `WHERE (title, id) < ($1, $2) ORDER BY title
DESC, id DESC LIMIT 3`, then reverses the two rows it keeps.

## `count=true`

Off by default (it costs a second scan). With `count=true` the statement
gains a `count(*)` subquery over the same `FROM`/`WHERE` and the total is
returned as `X-Total-Count`. It is distinct-aware (counts groups under
`distinct=`) and unaffected by `limit`, `offset`, `after`, `before`. `HEAD`
with `count=true` is the cheapest way to get just the number.

## `Prefer: count=` and `Content-Range`

PostgREST's counting preference is honoured on list `GET`/`HEAD` (tables,
views, `SETOF <table>` functions). Every mode fills both `X-Total-Count`
(kept for existing clients; `count=true` is `Prefer: count=exact`) and the
total of `Content-Range`; `Preference-Applied` names the mode actually used.

| Mode | Total | SQL (same statement) |
|---|---|---|
| `count=exact` | exact, after filters, distinct-aware | the `count(*)` subquery above |
| `count=planned` | `pg_class.reltuples` of the table — the planner's row statistic, as current as the last `ANALYZE` / autovacuum; for a partitioned table the sum over its leaf partitions (`pg_partition_tree`) | `(SELECT CASE WHEN pc.relkind = 'p' THEN coalesce(<sum of leaf reltuples>, <exact>) WHEN pc.relhassubclass OR pc.reltuples < 0 THEN <exact> ELSE pc.reltuples::bigint END FROM pg_catalog.pg_class pc WHERE pc.oid = '"public"."users"'::regclass)`; the exact count is used when the table (or any leaf partition) was never analysed (`reltuples = -1`), when a leaf is not a plain table (foreign / partitioned), and for legacy inheritance parents, whose statistic excludes the children a scan returns |
| `count=estimated` | exact while the table has at most `server.max_page_size` rows, planned above (never below `max_page_size + 1`) | a bounded `count(*)` over `LIMIT max_page_size + 1` rows, then the planned expression |

`planned`/`estimated` need an unfiltered scan of a table or materialized
view **without row-level security**: with filters, `distinct=`, `!inner`
embeds, on RLS-enabled tables (the statistic would reveal how many rows the
policies hide), on views and on functions pdbr counts exactly and answers
`Preference-Applied: count=exact`. The in-SQL fallbacks of the table above
(never analysed, inheritance parent) still report `count=planned`.
PostgREST runs `EXPLAIN` as a second statement for `planned`, which pdbr's
one-statement rule forbids (SQL cannot read a plan estimate inside a
query), so pdbr uses the table statistic and only where it is the right
number.

`Content-Range` is `<offset>-<offset + rows - 1>/<total or *>` and `*/<total
or *>` for an empty page; keyset pages (`after=`/`before=`) carry none
(their position is unknown). The status stays `200` — PostgREST answers
`206 Partial Content` when the range is a strict subset of a known total,
which RFC 9110 reserves for `Range` requests (not supported: the `Range`
request header is ignored).

```console
$ curl -sI -H 'Prefer: count=exact' 'localhost:8080/api/posts?limit=2&offset=2'
HTTP/1.1 200 OK
Content-Range: 2-3/5
Preference-Applied: count=exact
X-Total-Count: 5
```

Bulk `PATCH`/`DELETE` also carry `Content-Range` for the written rows
(`0-<n-1>/*`; total `n` with any `Prefer: count=`, always exact;
`*/…` under `return=minimal` or when nothing matched).

## `order=`

```
order  := term ("," term)*
term   := path [ "." dir ] [ "." nulls ]
path   := column | computed | relkey "(" column ")" | spatialcol ".dist"
        | column ( ".rank" | ".rank_cd" | ".trgm_dist" | ".trgm_wdist" | ".trgm_swdist" )
dir    := "asc" | "desc"
nulls  := "nullsfirst" | "nullslast"
```

```http
GET /api/posts?order=title                       -- ASC NULLS LAST
GET /api/posts?order=published.desc,title.asc    -- DESC NULLS LAST, ASC NULLS LAST
GET /api/users?order=mood.asc.nullsfirst
```

- Default direction `asc`. Nulls sort **last** in both directions unless
  `nullsfirst`/`nullslast` is given (pdbq parity; Postgres' own default for
  `DESC` is nulls first).
- The primary key is always appended as a tiebreaker so keyset paging is
  stable. Without `order=` rows come in PK order.
- Columns follow the [indexed-only policy](filtering.md#the-indexed-only-policy)
  (`@sortable` adds, `@omit order` removes); otherwise
  `400 column_not_orderable`. Unknown → `400 unknown_column`.

### Related columns

`advanced-filters` (setting `relation_order`) allows one level of related
ordering:

```http
GET /api/posts?order=users(email).asc            -- forward: by the author's email
GET /api/users?order=posts(title).desc           -- backward: MAX(title) over the user's posts
```

Forward relations compile to a correlated scalar subquery; backward relations
to an aggregate — `MIN` for `asc`, `MAX` for `desc`. Rows with no related row
sort as `NULL` (last, per the default above). The related column must be a
real column, orderable under its own table's policy (`400 unknown_column` /
`column_not_orderable`); ambiguous relations take hints
(`comments!parent_id(id).asc`). `@omit order` on the FK constraint comment
removes the relation from ordering in both directions (`400
column_not_orderable`), read from the catalog by the plugin itself; `@omit
many` removes the backward direction; `relations_opt_in` limits it to FKs
tagged `@filterable`/`@sortable`. Keyset cursors work under related
orderings (the anchor's value is re-evaluated by the same subquery). With
`relation_order: false` (or `relations: false`, or the plugin disabled) the
`rel(col)` syntax is `400 invalid_filter`.

### Computed columns

```http
GET /api/users?order=post_count.desc
```

Not index-backed; bypasses the policy like computed filters. Set-returning
computed columns are not orderable; `@omit order` on the function comment,
`computed_opt_in`, `computed: false` and the plugin being disabled all answer
`400 column_not_orderable`.

### Full-text relevance and trigram distance

```http
GET /api/articles?search=wfts(english).fat%20cat&order=search.rank.desc
GET /api/articles?order=title.trgm_dist&trgm=postgres&limit=10
```

`<col>.rank` / `<col>.rank_cd` order by `ts_rank` / `ts_rank_cd` against the
request's top-level full-text filter on the same column (missing or
ambiguous → `400 invalid_filter`); `<col>.trgm_dist` (and `trgm_wdist`,
`trgm_swdist`) by the `pg_trgm` distance to the `trgm=` parameter (missing →
`400 invalid_filter`), nearest-first under `asc`. Details and the index
policy for both in [contrib.md](contrib.md#ordering).

### Distance (`postgis` plugin)

```http
GET /api/places?order=location.dist.asc&near=SRID%3D4326%3BPOINT(-0.1281%2051.508)&limit=10
```

`<col>.dist` compiles to the KNN operator `<->` against `near=` (GeoJSON or
WKT/EWKT, cast to the column's flavour so geography columns rank in metres),
which a GiST index answers directly. `near` missing or unparseable → `400
invalid_filter` (deliberately stricter than pdbq, which drops the term
silently); one `near=` serves the root and every per-embed `.dist` term, and
a `near=` without any `.dist` term is `400 invalid_filter` too (on a route
with a column or argument named `near` the name is that filter instead,
and `.dist` is unavailable). The column follows the
indexed-only policy and `@omit order` like any other (`400
column_not_orderable`); plain `order=<spatialcol>.asc` is
`column_not_orderable` too, `.dist` on a non-spatial column
`invalid_filter`. Distance ordering is unaffected by the `nulls` modifier
(NULL geometries sort last); the PK tiebreaker and keyset cursors work as for
any order. Off with `plugins.settings.postgis.ordering: false` or the plugin
disabled (`400 invalid_filter`).

## `distinct=`

```http
GET /api/users?distinct=mood&order=mood.asc&select=mood
```

`distinct=col1,col2` compiles to `SELECT DISTINCT ON (col1, col2)`. The
distinct columns are moved to the front of `ORDER BY` (keeping the direction
you gave them, `asc` otherwise), so each group returns its first row under the
effective order. Constraints:

- Only `limit`/`offset` paging — `after`/`before` → `400` (de-duplication
  changes row identity, so cursors cannot anchor).
- Columns follow the indexed-only policy; spatial and composite columns are
  not allowed.
- `X-Total-Count` counts distinct groups.

## Cost limit

`server.max_cost` (default 10000) rejects requests whose estimated row
fan-out is too large, before any SQL runs:

```
cost         = 1 + Σ mult(child) × cost(child)   over the selected items
mult(list)   = explicit limit clamped to server.max_page_size, else 10   (root list, backward embed)
mult(other)  = 1                                                          (columns, forward embeds)
cost(column) = 1
```

An aggregated list (a root or embed selecting an [aggregate
function](aggregates.md#cost)) is costed as if it had no `limit` — the root
at `server.max_page_size`, an embed at 10 — because `limit` bounds the groups
it returns, not the rows the aggregate reads; each aggregate costs 1 like a
column, and a total on an aggregated root adds `server.max_page_size` × its
grouping keys.

`@costMultiplier n` on an FK comment replaces the multiplier of that backward
embed when `n` is lower — it only ever lowers an estimate (values < 1 are
ignored). Over the limit → `400 cost_exceeded`.

Example: `GET /api/users?select=id,posts(id,comments(id))&limit=20`:
`comments` = 1 + 10 × 1 = 11, `posts` = 1 + 10 × (1 + 11) = 121, request =
1 + 20 × (1 + 121) = 2441.

Two details the formula leaves implicit, fixed here: each named column
costs 1 and `*` costs the number of columns it expands to; a root list without
an explicit `limit` uses its **effective** limit (`server.max_page_size`,
default 100) as the multiplier, while a backward embed without an explicit
`limit` uses the estimate 10 (pdbq parity for nested lists).

## Per-embed pagination

Backward embeds are paged independently, with the same defaults and cap:

```http
GET /api/users?select=id,posts(id,title)&posts.limit=5&posts.offset=5&posts.order=title.desc
```

- `<rel>.limit` (default and cap `server.max_page_size`), `<rel>.offset`,
  `<rel>.order` (same grammar as `order=`, PK tiebreaker appended).
- Addressed by the relation key, or by the alias when the embed is aliased
  (`recent:posts(id)&recent.limit=3`).
- Embedded lists are offset-paged only; no cursors, no `count`.

## Views and PK-less resources

Views, materialized views and tables without a primary key page by
`limit`/`offset` only: `after`/`before` → `400 keyset_unsupported`, and
`Link` carries `offset=` links. `@primaryKey (cols)` in the view's comment
promotes it to a keyset-capable resource. `SETOF` RPC results are always
offset-only.

## Errors

| Code | Status | When |
|---|---|---|
| `page_size_exceeded` | 400 | `limit` (or `<rel>.limit`) above `server.max_page_size` |
| `keyset_unsupported` | 400 | `after`/`before` on a PK-less resource, `distinct=`, or a `SETOF` RPC; `offset` combined with `before` |
| `invalid_cursor` | 400 | `after`/`before` cursor undecodable or minted for a different resource |
| `column_not_orderable` | 400 | `order=`/`distinct=` column excluded by the policy or `@omit order` |
| `unknown_column` | 400 | unknown column / computed column in `order=` or `distinct=` |
| `unknown_relation` | 400 | relation key in an `order=` path or per-embed param matches no FK of the resource |
| `cost_exceeded` | 400 | estimated cost above `server.max_cost` |
| `invalid_filter` | 400 | malformed `order=` term, unknown direction/nulls token, `near` missing or unparseable |
| `ambiguous_relation` | 400 | related order path without a required hint |

Decided (M6): `order=` grammar errors keep `invalid_filter` (as
[errors.md](errors.md) does for a missing `near=`); no dedicated code — the
`code` vocabulary is closed for v1 and `detail` names the offending term.

See also [api.md](api.md) and [filtering.md](filtering.md).
