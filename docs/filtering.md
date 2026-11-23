# Filtering

Any query parameter that is not reserved (`select`, `order`, `limit`,
`offset`, `after`, `before`, `count`, `distinct`, `on_conflict`, and every
name starting with `~`, see [api.md](api.md#the--namespace)) is a
filter. Filters AND together, become `WHERE` predicates in the single compiled
statement, and every operand travels as a bind parameter — the SQL text never
varies with input values (an invariant enforced by fuzz tests).

```
filter := column [ jsonpath ] "=" [ "not." ] op [ "(" config ")" | "(" mod ")" ] "." value
       |  relkey [ "!" quantifier ] "." column "=" [ "not." ] op "." value
       |  relkey "=" "exists." ( "true" | "false" )
       |  ( "and" | "or" | "not.and" | "not.or" ) "=" "(" item ("," item)* ")"
item   := column [ jsonpath ] "." [ "not." ] op [ "(" mod ")" ] "." value | logic "(" item ("," item)* ")"
mod    := "any" | "all"                                  -- eq, gt, gte, lt, lte, like, ilike, match, imatch
```

`jsonpath` is the `select=` grammar (`->key`, `->0`, `->>key`;
[api.md](api.md#json-paths)) — see [JSON paths](#json-paths).

```http
GET /users?mood=eq.happy&email=ilike.*@example.com
GET /posts?published=is.true&title=not.like.Draft*
GET /users?id=in.(1,2,3)&tags=cs.{admin}
```

The value is everything after `op.`; it is coerced to the column's type at
compile time (`400 invalid_filter` when it does not parse) and passed as
`$n`. Enum values are pg labels verbatim.

## Operators

| op | SQL | Types |
|---|---|---|
| `eq`, `neq` | `=`, `<>` | scalars, enums |
| `gt`, `gte`, `lt`, `lte` | `>`, `>=`, `<`, `<=` | scalars, enums |
| `in.(a,b,"c,d")` | `= ANY($n)` (typed array param) | scalars, enums |
| `not.in.(…)` | `<> ALL($n)` | scalars, enums |
| `is.null`, `is.true`, `is.false` | `IS NULL`, `IS TRUE`, `IS FALSE` (structural, no parameter) | any / bool |
| `not.is.null` … | `IS NOT NULL` … | any / bool |
| `isdistinct` | `IS DISTINCT FROM $n` (`not.isdistinct` → `NOT (… IS DISTINCT FROM $n)`) | wherever `eq` applies |
| `like`, `ilike` | `LIKE $n`, `ILIKE $n`; `*` in the value → `%` | text |
| `sw`, `ew` | `LIKE $n \|\| '%'`, `LIKE '%' \|\| $n` — operand fully escaped | text |
| `match`, `imatch` | `~ $n`, `~* $n` — POSIX regular expression | text |
| `op(any).{a,b}`, `op(all).{a,b}` | `op ANY($n)`, `op ALL($n)` (typed array param) for `eq`, `gt`, `gte`, `lt`, `lte`, `like`, `ilike`, `match`, `imatch` — see [Modifiers](#modifiers-any-and-all) | scalars, enums (not arrays) |
| `cs`, `cd`, `ov` | `@>`, `<@`, `&&` | arrays (`{a,b}` literal); `cs`/`cd` also jsonb (JSON literal); ranges, hstore, ltree, inet — see [contrib.md](contrib.md) |
| `fts`, `plfts`, `phfts`, `wfts` (optional `(config)`) | `@@ to_tsquery` / `plainto_tsquery` / `phraseto_tsquery` / `websearch_to_tsquery` | `tsvector`; text-like through `to_tsvector` — [contrib.md](contrib.md#full-text-search-postgrest) |
| `sl`, `sr`, `nxr`, `nxl`, `adj` | `<<`, `>>`, `&<`, `&>`, `-\|-` (range literal or JSON object operand) | ranges, multiranges — [contrib.md](contrib.md#ranges-and-multiranges-postgrest) |
| contrib (pdbr) | `trgm_sim`, `trgm_wsim`, `trgm_swsim` (pg_trgm); `unaccent_eq`, `unaccent_ilike`, `unaccent_sw`, `unaccent_includes` (unaccent, text-like columns); `levenshtein_distance_lt`, `soundex`, `metaphone`, `dmetaphone` (fuzzystrmatch, text-like columns); `query_int` (intarray, `int4[]`); `cs`, `cd`, `ov`, `l2_distance_lt`, `l1_distance_lt`, `chebyshev_distance_lt` (cube); `earth_distance_lt` (earthdistance `earth`), `earth_miles_distance_lt` (`point` with earthdistance); `intersects` (PostGIS raster, [postgis.md](postgis.md#rasters)); `hk_all`, `hk_any`, `hk_def` (hstore); `ancestor`, `descendant`, `ancestor_any`, `descendant_any`, `lquery`, `lquery_any`, `ltxtquery` (ltree); `cs_strict`, `cd_strict` (inet) — see [contrib.md](contrib.md) | extension types, gated on the installed extension |
| `hk` | `?` (has key) | jsonb, hstore |
| `jpe`, `jpm` | `@?`, `@@` — value is a SQL/JSON path | jsonb |
| spatial (`postgis` plugin) | `st_intersects`, `st_disjoint`, `st_contains`, `st_contains_properly`, `st_within`, `st_covers`, `st_covered_by`, `st_crosses`, `st_overlaps`, `st_touches`, `st_equals`, `st_exactly_equals`, `st_ordering_equals`, `st_3d_intersects`, `bbox_left_of`, `bbox_right_of`, `bbox_below`, `bbox_above`, `bbox_overlaps_or_left_of`, `bbox_overlaps_or_right_of`, `bbox_overlaps_or_below`, `bbox_overlaps_or_above`, `bbox_equals`, `bbox_intersects_nd`, `bbox_intersects` (value = GeoJSON or WKT/EWKT); `st_dwithin.(geom,dist[,spheroid])`, `st_beyond.(…)`; `bbox_intersects.(minx,miny,maxx,maxy[,srid])`, `bbox_contains`, `bbox_contained_by` — plus `is.null`; scalar ops on a spatial column are `400 invalid_filter`, spatial ops on a scalar column too; off (`invalid_filter`) with `plugins.settings.postgis.filters: false` or the plugin disabled — see [postgis.md](postgis.md) | geometry / geography |
| relation (`advanced_filters` plugin) | `EXISTS` subqueries — see [Relation filters](#relation-filters) | FKs |
| computed (`advanced_filters` plugin) | `fn(t) op $n` — see [Computed columns](#computed-columns) | computed columns |

`not.` may prefix any op. Unknown op → `400 invalid_filter`; an op applied to a
type that does not support it (e.g. `like` on `int4`, `eq` on geometry) →
`400 invalid_filter`.

### Text patterns

- `like`/`ilike`: `*` is translated to `%`; a literal `%` is passed through
  (URL-encode it as `%25`).
- `sw`/`ew`: the operand is escaped (`%`, `_`, `\`) so it always matches
  literally; the wildcard is added in SQL. Prefer these over `like` when the
  value comes from user input.

As built: `_` inside `like`/`ilike` values is passed through as the SQL
single-character wildcard (PostgREST behaviour) and a literal `%` passes
through too — only `*` is translated (`compile.likePattern`); escape `_` as
`\_` to match literally. `sw`/`ew` operands are fully escaped
(`compile.escapeLike`: `\`, `%`, `_`).

### Regular expressions: `match`, `imatch`

`match` is PostgreSQL's `~` (case-sensitive POSIX regular expression),
`imatch` `~*` (case-insensitive); the pattern is a bind parameter, unanchored
unless it uses `^`/`$`:

```http
GET /users?email=match.^a.*@example\.com$
GET /posts?title=imatch.engine
```

An invalid pattern is a database error (`400 pg:2201B`). On a `citext`
column the extension's own operators apply (`OPERATOR(ext.~)` with a
`citext` operand), so `match` is case-insensitive there too, as
PostgreSQL resolves it. Like `jpe`/`jpm`, a pathological pattern costs CPU
until `database.statement_timeout`.

### Modifiers: `any` and `all`

`eq`, `gt`, `gte`, `lt`, `lte`, `like`, `ilike`, `match` and `imatch` take
an `(any)` or `(all)` modifier and a PostgreSQL array literal (`{a,b}`,
items quoted as in array filters); the condition holds when the operator
matches any / every element:

```http
GET /people?last_name=like(any).{O*,P*}       -- last_name LIKE ANY($1::text[])
GET /people?last_name=like(all).{O*,*n}       -- LIKE ALL
GET /users?id=gt(all).{1,2}                   -- id > ALL($1::int4[])
GET /users?mood=eq(any).{ok,sad}              -- like in.(ok,sad)
GET /users?or=(email.match(any).{^a,^g},id.eq(all).{3})
```

Elements are coerced to the column type (enums, citext and other extension
types get the same array casts as `in.()`), `*` becomes `%` in every
`like`/`ilike` element, `NULL` items stay SQL `NULL`; `{}` is false under
`any` and true under `all`. The modifier needs a `{…}` list and a scalar
column (`400 invalid_filter` on arrays, on other operators, or with another
word than `any`/`all`); `not.` negates the whole condition
(`NOT (col = ANY($1))`).

### Lists: `in.(…)`

Items are comma-separated inside the parentheses. An item containing `,`,
`(`, `)` or `"` is double-quoted; inside quotes `\"` and `\\` are the only
escapes:

```http
GET /users?email=in.(a@b.c,"x,y@z.io")
GET /users?mood=not.in.(sad,ok)
```

The whole list becomes one typed array parameter (`= ANY($1::text[])`), so the
SQL text is identical for one item or a thousand.

### Arrays, jsonb, jsonpath

```http
GET /users?tags=cs.{admin,ops}                       -- tags @> '{admin,ops}'
GET /users?tags=ov.{ops}                             -- tags && '{ops}'
GET /users?settings=cs.{"theme":"dark"}              -- settings @> '{"theme":"dark"}'
GET /users?settings=hk.theme                         -- settings ? 'theme'
GET /users?settings=jpe.$.notifications[*]?(@=="email")
GET /users?settings=jpm.$.theme=="dark"
```

Array values use the Postgres array literal (`{a,b}`); jsonb `cs`/`cd` take a
JSON literal. `json` columns are cast to `jsonb` for `hk`/`jpe`/`jpm`.
`jpe`/`jpm` execute a caller-supplied jsonpath: the value is a bind parameter
(no injection), but an expensive path can burn CPU until
`database.statement_timeout` stops it — remove the ops with `@omit filter` on
the column or keep the timeout tight when serving untrusted callers.

### JSON paths

A filter key may continue a `json`/`jsonb` or array column with a JSON path
(as in `select=`): `->>` compares the value as **text** (every text operator:
`eq`, `neq`, `gt`…`lte`, `in`, `is`, `isdistinct`, `like`, `ilike`, `sw`,
`ew`), `->` compares it as **jsonb** (`eq`, `neq`, `gt`…`lte`, `isdistinct`
and `in` with JSON operands, `cs`/`cd` with a JSON literal, `hk`, `jpe`,
`jpm`, `is.null`):

```http
GET /users?settings->>theme=eq.dark                 -- (settings ->> $1::text) = $2
GET /users?settings->age=gt.20                      -- (settings -> $1::text) > $2  (jsonb 20)
GET /users?settings->theme=in.("dark","light")      -- = ANY($2::jsonb[]); quoted items are JSON strings
GET /users?settings->prefs=cs.{"mail":true}
GET /users?tags->>0=eq.admin                        -- arrays go through to_jsonb(tags)
GET /users?or=(settings->>theme.eq.dark,settings->"a.b".is.null)
GET /posts?users.settings->>theme=eq.dark           -- relation / embed filters too
```

A `->` operand is JSON: `settings->theme=eq.dark` is `400 invalid_filter`;
write the JSON string (`settings->theme=eq."dark"`) or compare as text
(`settings->>theme=eq.dark`).
Keys and indexes are bind parameters, like the operand. The column must be
filterable under the filter policy (`filters.indexed_only`: an expression
index is not detected, so list the column in `filters.allow_columns` or
index the column itself); `order=` takes the same paths under the order
policy. Contrib and spatial operators do not apply to paths
(`400 invalid_filter`); composite columns take paths in `select=` only
(they are never filterable or orderable, `400 column_not_filterable`).
On an hstore column the path is one key, `attrs->color` or
`attrs->>color` alike, compared as **text** (the value of that key; see
[contrib.md](contrib.md#hstore-pdbr)).

Enums:

```http
GET /users?mood=eq.happy
GET /users?mood=in.(happy,ok)
```

As built: composite-typed columns (`address`) are not filterable at all
(pdbq parity), including `is.null` — they are excluded from
`Resource.FilterColumns` regardless of policy (`400 column_not_filterable`).

## Logic: `and=`, `or=`, `not.and=`, `not.or=`

Items inside a logic list use dots throughout — `col.[not.]op.value` — and
nest arbitrarily:

```http
GET /users?or=(mood.eq.happy,mood.is.null)
GET /users?and=(mood.in.(happy,ok),or(email.ew.@example.com,tags.cs.{admin}),not.settings.hk.banned)
GET /posts?not.or=(published.is.false,title.sw.Draft)
```

Compiles to `WHERE (mood = $1 OR mood IS NULL)`, `WHERE (mood = ANY($1) AND
(email LIKE '%' || $2 OR tags @> $3) AND NOT (settings ? $4))`, and
`WHERE NOT (published IS FALSE OR title LIKE $1 || '%')`.

Quoting: a value containing `,`, `(` or `)` is double-quoted
(`title.eq."Hello, world (draft)"`); backslash escapes `\"` and `\\` inside
quotes. Top-level filters (`title=eq.Hello, world`) need no quoting — the
value runs to the end of the parameter.

A column named like a reserved parameter (`select`, `order`, …) can only be
filtered inside a logic list: `and=(select.eq.x)`.

## Relation filters

Provided by the built-in `advanced_filters` plugin (priority 150; settings
`relations`, `relations_opt_in`). Relation keys are table names, exactly as in
`select=` embeds ([api.md](api.md#naming)); ambiguous relations take the same
hints (`comments!parent_id.body=eq.x`).

| Form | Meaning | SQL |
|---|---|---|
| `posts.title=eq.x` | *some* related row matches | `EXISTS (SELECT 1 FROM posts p WHERE p.author_id = u.id AND p.title = $1)` |
| `posts!none.title=eq.x` | *no* related row matches | `NOT EXISTS (… AND p.title = $1)` |
| `posts!every.title=eq.x` | every related row matches (vacuously true with none) | `NOT EXISTS (… AND NOT (p.title = $1))` |
| `users.email=eq.x` | forward: the referenced row matches | `EXISTS (SELECT 1 FROM users r WHERE r.id = p.author_id AND r.email = $1)` |
| `posts=exists.true` / `exists.false` | at least one / no related row | `EXISTS (…)` / `NOT EXISTS (…)` |
| `users=exists.true` / `exists.false` | referenced row present / absent (FK null or dangling) | `EXISTS (…)` / `NOT EXISTS (…)` |

- **Backward** (FK points at this table): quantifiers `some` (default),
  `!none`, `!every` apply. **Forward** (FK on this table): a single referenced
  row, so quantifiers are rejected (`400 invalid_filter`); `exists.false` is
  the "FK null or dangling" test. A forward relation over a PostgreSQL 18
  `PERIOD` foreign key references every row whose period overlaps, so it
  takes quantifiers like a backward one; its correlation is `r.k = t.k AND
  r.valid_at && t.valid_at`
  ([api.md](api.md#temporal-keys-and-postgresql-18-foreign-keys)).
- Several conditions on the same relation key and quantifier are ANDed inside
  one `EXISTS`: `posts.published=is.true&posts.title=sw.A` finds users with a
  post that is both published *and* starts with `A`. `not.` on the leaf
  negates the leaf (`posts.title=not.eq.x` — some post whose title differs);
  `posts=not.exists.true` ≡ `posts=exists.false`.
- Relation filters also work inside logic lists
  (`or=(posts.title.eq.x,id.eq.3)`); each item is its own `EXISTS`.
- The related column follows its own table's
  [indexed-only policy](#the-indexed-only-policy) (`users?posts.published=…`
  is `400 column_not_filterable` under the default policy) and may itself be
  a computed column of the related table.
- Paths are **one level deep**: `posts.comments.body=eq.x` is
  `400 invalid_filter`. (The compiler already renders nested `EXISTS`
  chains; the grammar stays one-level until a use case needs more.)
- Per-object switches, read straight from the catalog by the plugin
  (they work with `smart_comments` disabled): `@omit filter` on the FK
  constraint comment removes the relation from the filter surface in both
  directions (`400 column_not_filterable`), `@omit` on the FK removes
  everything; `@omit many` (smart_comments) removes the backward direction
  along with the embed. With `relations_opt_in: true` only FKs tagged
  `@filterable` (alias `@sortable`) get relation filters.
- With `relations: false` or the plugin disabled, the syntax itself is
  rejected: `400 invalid_filter` for `rel.col=`, `rel!none.`, `rel=exists.`
  on a known relation key (`unknown_relation` for an unknown key, as always).
  Embeds and embed filters (`select=posts(id)&posts.title=…`) are core and
  keep working.

### Interaction with embeds

A filter whose path matches an embed in `select=` is an *embed filter*, not a
relation filter — it narrows the embedded list (backward) or nulls out the
object (forward) without touching the parent:

```http
GET /users?select=id,posts(id)&posts.published=is.true      -- all users; only published posts embedded
GET /users?select=id,posts!inner(id)&posts.published=is.true -- only users with a published post
GET /users?posts.published=is.true                           -- no embed: pure relation filter → EXISTS
```

With `!inner` the same parameter does both: the embed is restricted **and**
the parent is restricted (compiled as an `EXISTS` on the parent plus the
inner lateral). Quantified forms (`posts!none.`, `posts!every.`) and
`exists.` are always relation filters and never address an embed.

## Computed columns

`advanced_filters` (setting `computed`, `computed_opt_in`) exposes computed
columns to filters and ordering:

```http
GET /users?post_count=gt.3
GET /users?post_count=in.(0,1)&order=post_count.desc
```

Compiles to `users_post_count(u) > $1`; the operand is coerced by the
function's return type exactly like a column of that type. Set-returning
computed columns (`SETOF <type>`, `SETOF <table>`) are never filterable or
orderable (`400 column_not_filterable` / `column_not_orderable`). Computed
and relation filters cannot be index-backed by nature and bypass the
indexed-only policy — a documented exception; `@omit filter` / `@omit order`
on the function comment (read from the catalog by the plugin itself), or the
`computed_opt_in` setting (only functions tagged `@filterable`/`@sortable`),
close them off. The computed surface also follows `schema.functions`: when
functions are not exposed there is nothing to filter. With `computed: false`
or the plugin disabled, `post_count=gt.3` is `400 column_not_filterable` and
`order=post_count` is `400 column_not_orderable`; `select=post_count` is
core and stays.

## The indexed-only policy

By default **only columns covered by an index** (leading column, plus PK and
unique-constraint columns) are filterable and orderable — an accidental
sequential-scan API cannot be built. Filtering a non-indexed column returns
`400 column_not_filterable` (`column_not_orderable` in `order=`).

```yaml
filters:
  indexed_only: true
  allow_columns:
    public.posts: [published]   # extra columns despite the policy
```

- `filters.indexed_only: false` exposes every column.
- `filters.allow_columns` per `schema.table` adds columns.
- `@filterable` / `@sortable` on a column comment adds it (synthetic index in
  the catalog); `@omit filter` / `@omit order` removes it regardless.
- Filters on embedded resources and relation/order paths follow the
  *embedded* table's policy.
- Spatial columns need a GiST index under the policy, like any other column.
- Contrib operators (full-text, trigram, hstore, ltree, range, inet) also
  need an index whose access method serves the operator, or the column in
  `allow_columns`; full-text search on a plain text column always needs
  `allow_columns` (see [contrib.md](contrib.md#the-indexed-only-policy)).

## Errors

| Code | Status | When |
|---|---|---|
| `unknown_column` | 400 | column or computed column does not exist on the resource |
| `unknown_relation` | 400 | relation key in a filter path matches no FK of the resource |
| `column_not_filterable` | 400 | excluded by the indexed-only policy or `@omit filter`; relation/computed column closed by `@omit filter`, opt-in mode, `SETOF`, or the `computed` setting / plugin off |
| `invalid_filter` | 400 | unknown op, op/type mismatch, unparseable value, bad `in.()`/logic syntax, quantifier on a to-one (forward, non-`PERIOD`) relation, relation path deeper than one level, relation syntax while `relations` is off / the plugin is disabled |
| `ambiguous_relation` | 400 | relation key matches several FKs and no hint given |

Errors are RFC 9457 problem documents; see [api.md](api.md#limits-and-errors).

## Differences from PostgREST

- Short op names match PostgREST (`eq`, `neq`, `gt`, `like`, `match`,
  `imatch`, `cs`, `ov`, `isdistinct`, `fts`/`plfts`/`phfts`/`wfts` with
  `(config)`, the range ops `sl`/`sr`/`nxr`/`nxl`/`adj`, the `any`/`all`
  modifiers, …) but pdbr adds `sw`/`ew` (escaped prefix/suffix), `hk`,
  `jpe`/`jpm`, the trigram/hstore/ltree/inet words of
  [contrib.md](contrib.md), and the spatial and relation vocabularies;
  full-text search applies to `tsvector` and text columns only (not
  json/jsonb). `match`/`imatch` need a text column (`400 invalid_filter`
  elsewhere), and the modifiers are validated element by element before the
  database sees them. inet `cs`/`cd` mean `>>=`/`<<=` (containment, as on
  other types) rather than PostgREST's literal `@>`/`<@`.
- Relation filters have quantifiers (`rel!none.`, `rel!every.`) and an
  existence op (`rel=exists.true|false`); PostgREST only filters through
  `!inner` embeds.
- JSON path filters and `order=` terms need a filterable / orderable
  column; composite columns are neither, so `address->>city=eq.x` is
  `400 column_not_filterable` (PostgREST filters `to_jsonb(address)`).
  A `->` operand must be valid JSON (`400 invalid_filter`, not a database
  error), and `in.()` on a `->` path takes JSON items.
- Computed columns are addressed by the function name minus the `<table>_`
  prefix, not the full function name.
- Non-indexed columns are rejected by default (`filters.indexed_only`).

## Differences from pdbq

pdbq exposes filters as a typed GraphQL input; pdbr uses the same operator
semantics under short URL names:

| pdbq | pdbr |
|---|---|
| `equalTo`, `notEqualTo` | `eq`, `neq` |
| `lessThan`, `lessThanOrEqualTo`, `greaterThan`, `greaterThanOrEqualTo` | `lt`, `lte`, `gt`, `gte` |
| `in`, `notIn` | `in.(…)`, `not.in.(…)` |
| `isNull: true/false` | `is.null`, `not.is.null` |
| `like`, `likeInsensitive` | `like`, `ilike` (`*` → `%`) |
| `startsWith`, `endsWith` | `sw`, `ew` |
| `contains`, `containedBy`, `overlaps` | `cs`, `cd`, `ov` |
| `containsKey`, `pathExists`, `pathMatch` | `hk`, `jpe`, `jpm` |
| `and: []`, `or: []`, `not: {}` | `and=(…)`, `or=(…)`, `not.and=(…)`/`not.or=(…)` |
| `condition: {col: v}` | plain `col=eq.v` (no separate shorthand) |
| `posts: {some\|none\|every: {…}}` | `posts.col=…`, `posts!none.col=…`, `posts!every.col=…` |
| `postsExist: true`, `authorExists: false` | `posts=exists.true`, `users=exists.false` |
| `bboxIntersects`, `intersects`, `dwithin`, `bboxLeftOf`, `exactlyEquals`, `intersects3D`, … | `bbox_intersects.(…)`, `st_intersects`, `st_dwithin.(…)`, `bbox_left_of`, `st_exactly_equals`, `st_3d_intersects`, … (snake_case, `bbox_`/`st_` prefix — [postgis.md](postgis.md#filtering)) |
| relation keys `author` / `postsByAuthorId` | table names `users` / `posts` (+ `!hint`) |
| enum values `HAPPY` | labels verbatim `happy` |

Policy (`filters.indexed_only`, `allow_columns`, `@filterable`, `@omit
filter`) and the one-statement/EXISTS compilation are identical.

Next: [pagination.md](pagination.md) for `order=`, `limit`, cursors and
`distinct=`.
