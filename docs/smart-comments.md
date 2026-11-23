# Smart comments

The built-in `smart-comments` plugin (enabled by default, priority 50) reads
PostGraphile-style tags out of ordinary PostgreSQL `COMMENT`s and customizes
the generated REST API — rename, hide, deprecate, and extend resources with
plain DDL, no pdbr configuration, no redeploy (watch mode picks comment
changes up like any other DDL).

The tag vocabulary is pdbq's, verbatim; only the effects change: where a tag
removed a GraphQL field or type, it now removes a route, a method, a query
parameter, an embed key, or an OpenAPI schema. Both products parse tags with
`pdbcore/smarttags` and apply the catalog-level effects with
`pdbcore/smartcomments/catalog`, so one set of comments serves both.

## Syntax

Lines at the **start** of a comment beginning with `@` are tags; everything
after the first non-tag line is the description (tag lines never leak into
OpenAPI descriptions):

```sql
COMMENT ON TABLE app.users IS E'@name people\n@omit delete\nA person with an account.';
```

Parsing rules (`pdbcore/smarttags.Parse`):

- A comment whose first non-blank character is not `@` has no tags; the
  whole text is the description.
- Each tag line is `@name[ value]` — the name ends at the first space or tab,
  the rest (trimmed) is the value. A bare tag has an empty value.
- Blank lines between tag lines do not end the tag block; the first
  non-blank, non-`@` line does. Everything from that line on is the
  description (trimmed).
- Tags may repeat where noted; each occurrence keeps its own value.
- List-valued tags (`@omit`, `@primaryKey`, `@unique`, `@behavior`) split on
  commas and whitespace: `@omit create, update` ≡ `@omit create update`.
  `@omit` actions are lower-cased.
- `@behavior` (PostGraphile V5 style) contributes only its **negative**
  tokens, mapped onto `@omit` actions: `-insert` → `create`, `-update` →
  `update`, `-delete` → `delete`, `-select` → `read`, `-filter` → `filter`,
  `-order` → `order`; other tokens are ignored.

Disable everything with `plugins.disabled: [smart-comments]` (see
[What survives without the plugin](#what-survives-without-the-plugin)).

## Placement

| Statement | Applies to |
|---|---|
| `COMMENT ON TABLE t` / `COMMENT ON VIEW v` / `COMMENT ON MATERIALIZED VIEW mv` | resource-level tags |
| `COMMENT ON COLUMN t.c` | column tags |
| `COMMENT ON CONSTRAINT name ON t` | foreign-key and unique-constraint tags |
| `COMMENT ON FUNCTION f(args)` | RPC and computed-column tags |
| `COMMENT ON TYPE e` | enum tags |

## Effect summary

| Tag | Applies to | REST effect |
|---|---|---|
| `@omit` | table, column, FK, unique, function | remove the object everywhere (routes, schema, embeds, params) |
| `@omit read` | table, column | table: no `GET`/`HEAD` on `/{t}`, `/{t}/{pk}`, `/{t}/by/…` (still embeddable); column: absent from responses, `select=`, filters, `order=`, `distinct=`, aggregates, `/by/…` routes, cursors and nested-mutation keys; a FK referencing it offers no nested `create` / `connect` / `disconnect` (they would copy the hidden value into the readable FK column) |
| `@omit all` | table | no `GET`/`HEAD /{t}` collection; row paths stay |
| `@omit many` | table, FK | not embeddable/filterable/orderable as a **backward** relation, no backward nested-mutation key |
| `@omit create` / `update` / `delete` | table, column | table: method removed → `405` + `Allow`, and the matching nested-mutation operation (`create`, `update` / backward `connect` / `disconnect`, `delete`) on that table → `400 unknown_relation`; column: key rejected in that body (`400`), including nested rows and patches, and on a FK column no nested operation that would set it in a body of that kind |
| `@omit filter` / `@omit order` | table, column, FK, computed | filter params / `order=` values removed → `400` if used |
| `@name x` | table, column, function, enum | path segment / JSON key / RPC path / schema name |
| `@fieldName x` / `@foreignFieldName y` | FK | forward / backward relation key (embeds, filter & order paths, nested-mutation body keys) |
| `@deprecated [reason]` | column, function, table | OpenAPI `deprecated: true` (+ `x-deprecation-reason`) |
| `@notNull` / `@nullable` | column | OpenAPI nullability + `.insert` required list |
| `@filterable` (alias `@sortable`) | column, FK, computed | filter/order allowed despite `filters.indexed_only`; opt-in for advanced-filters |
| `@primaryKey cols` | view / PK-less table | row path `/{t}/{pk}`, keyset cursors, ETag |
| `@unique cols` | view / table | `/{t}/by/{cols}/{vals}` route |
| `@foreignKey (…) references …` | view / table | embeds both ways, relation filters/order, nested-mutation keys |
| `@enum` / `@enumDescription` | table / column | lookup table becomes an OpenAPI enum; resource disappears |
| `@costMultiplier n` | FK | cost estimate for the backward embed |
| `@anyelement t` / `@anycompatible t` | function | concrete type of a polymorphic signature: the function becomes an RPC typed with `t` |
| `@behavior -tokens` | any of the above | aliases for `@omit` actions |

## Tag reference

### Tables and views

| Tag | Effect |
|---|---|
| `@omit` | Hide the table entirely: no routes, no `components.schemas` entry, not embeddable, no relation keys or filters referencing it, no function returning or taking its rows (RPCs and computed columns, also through a resolved `@anyelement` tag), and no column typed as its row type, directly or through a domain, a composite attribute or an array. |
| `@omit read` | (table) No `GET`/`HEAD` on `/{table}`, `/{table}/{pk}` and `/{table}/by/…`; the resource stays reachable through `select=` embeds from related resources — useful for detail/join tables. Write methods on those paths are unaffected. |
| `@omit all` | No collection `GET`/`HEAD /{table}`; `/{table}/{pk}` and `/{table}/by/…` stay. |
| `@omit many` | Never embedded, filtered or ordered as a backward relation from other resources (no `users?select=posts(*)`, no `users?posts.title=…`); forward embeds (`posts?select=users(*)`) stay. |
| `@omit create` / `update` / `delete` | Suppress the corresponding method — `POST /{table}`, `PATCH /{table}/{pk}`, `DELETE /{table}/{pk}` — exactly like a missing SQL privilege: `405` with `Allow` listing what remains. `PUT /{table}` (upsert) needs both create and update, so either omit removes it. |
| `@omit filter` / `@omit order` | Drop every filter parameter / the `order=` parameter for the resource (top level and embedded); using one → `400`. |
| `@name <new_name>` | Rename the resource: path segment (`/people`, `/{schema}/people`), `components.schemas` key (`people`, `people.insert`, …), and — because relation keys are table names — the embed/filter key other resources use (`posts?select=people(*)`). Use a name shaped like a table name. |
| `@primaryKey col[, col]` | Declare a logical primary key on a view (or PK-less table): enables `/{view}/{pk}`, keyset pagination (`after`/`before`, `X-Next-Cursor`), and `ETag` on the row path. Mutations remain gated by real privileges, so views stay read-only. |
| `@unique col[, col]` | Declare a logical unique constraint: generates `/{view}/by/{cols}/{vals}` (repeatable). |
| `@foreignKey (col, …) references [schema.]table (col, …)` | Declare a logical foreign key (typically on views): the view gains a forward embed key, the referenced table gains a backward embed key, and `advanced-filters` treats it like a real FK (repeatable). Append `\|@fieldName x\|@foreignFieldName y` to name the keys. |
| `@enum` | Treat the table as an enum: its **rows** become the values. See [Enum tables](#enum-tables). |
| `@deprecated [reason]` | Every operation on the resource's paths gets `deprecated: true` (+ `x-deprecation-reason`). |

`@omit` actions combine: `@omit create, delete` or repeated `@omit` lines.

`@omit read` leaves `POST`/`PATCH`/`DELETE` on the paths (a write-only
resource answers `GET` with `405` and an `Allow` of the write methods; a
resource with no methods left has no routes at all but stays embeddable and
keeps its `components.schemas` entry), and `PUT` needs both `create` and
`update`. `@omit all` keeps the collection path for its write methods
(`GET /{table}` → `405`, `Allow: POST, PUT`; nothing left → `404`).

### Columns

| Tag | Effect |
|---|---|
| `@omit` | Hide the column everywhere (responses, `select=`, request bodies, filters, `order=`, OpenAPI, the [ETag](api.md#etag--conditional-requests) hash). It may still back FKs and the join columns of embeds. |
| `@omit read` | Hide it from everything that could reveal its value: responses, `select=` (including aggregate inputs, grouping and spreads), the [ETag](api.md#etag--conditional-requests) hash, and, **unlike PostGraphile and PostgREST column privileges**, every query surface: filters (conditions, `or`/`and`/`not` trees, JSON paths, operators of the contrib and spatial plugins, embed and relation filters), `order=`, `distinct=` and aggregates are all `400 unknown_column`, as is `on_conflict=` naming it. Otherwise a hidden `salary` could be recovered by bisecting with `salary=gt.50000`. Rows of the table nested in other values (an array of rows, a composite attribute, a `RETURNS TABLE` column, a computed column returning the row) are rendered through the resource's readable columns, so the column stays hidden there too ([rpc.md](rpc.md#return-shapes)). It stays writable in `POST`/`PUT`/`PATCH` bodies. On a raster column (never readable anyway) it removes the metadata, pixel value and statistics pseudo-columns and the `intersects` filter ([postgis.md](postgis.md#rasters)). |
| `@omit create` / `update` / `filter` / `order` | Hide it from just that surface: `create`/`update` → key rejected in `POST`/`PUT` or `PATCH`/`PUT` bodies (`400 invalid_body`, `column "c" of "t" cannot be written by POST`); `filter` → `400 column_not_filterable`; `order` → `400 column_not_orderable`. |
| Unique and primary keys over a hidden column | A unique constraint over a `read`-omitted (or fully omitted) column loses its `/{table}/by/{cols}/…` route (`404`) and its `on_conflict=` target. A primary key containing one loses the `/{table}/{pk}` row path (`404`), keyset paging (`after=`/`before=` → `400 keyset_unsupported`, no `X-Next-Cursor`/`Link` cursors) and the `Location` header, since a cursor or path would expose or probe the value; page such tables with `limit`/`offset`. Rows still sort by the key as a tie-breaker. A `NOT NULL` column without a default tagged `@omit create` makes every `POST` fail its required-column check — omit the column from reads instead, or give it a default. |
| `@name <new_name>` | Rename the JSON key everywhere: responses, `select=`, filter param name, `order=` value, body keys, `/by/{cols}` segments, OpenAPI property. |
| `@deprecated [reason]` | OpenAPI property `deprecated: true`, plus `x-deprecation-reason: <reason>` (`No longer supported` when the tag has no value). |
| `@notNull` / `@nullable` | Override introspected nullability — views lose `NOT NULL`, `@notNull` restores `type: T` (instead of `[T, "null"]`) in OpenAPI. On a table column, `@notNull` without a default also makes the key `required` in `<name>.insert`; `@nullable` removes it from `required`. |
| `@filterable` (alias `@sortable`) | Admit the column to filtering **and** ordering despite `filters.indexed_only` — the per-column form of `filters.allow_columns`. Combine with `@omit filter` or `@omit order` to expose only one side. |

### Constraints (`COMMENT ON CONSTRAINT … ON <table>`)

| Tag | Effect |
|---|---|
| `@omit` on a foreign key | Remove both relation keys (no embeds either way), the relation filters/order, and the nested-mutation body keys. |
| `@omit many` on a foreign key | Remove only the backward side (`users?select=posts(*)`, `users?posts.title=…`, `users?order=posts(title).asc`). |
| `@omit filter` on a foreign key | Keep the embed keys, drop the relation filter (both directions). |
| `@omit order` on a foreign key | Keep the embed keys, drop related-column ordering (`order=users(name).asc`). |
| `@fieldName <name>` / `@foreignFieldName <name>` | Exact relation keys for the forward / backward side. They replace the default table-name keys in `select=` embeds, filter and order paths, and nested-mutation body keys, and make a `!hint` unnecessary even when the relation is ambiguous. Applied through the shared naming pipeline, so `advanced-filters` and `nested-mutations` see the same key. |
| `@filterable` on a foreign key | Opt the relation into filtering when `advanced-filters` runs in opt-in mode (below). |
| `@costMultiplier <n>` on a foreign key | Expected rows per parent on the backward side, used by the cost estimator instead of the requested `limit`. See [Cost estimation for recursive relations](#cost-estimation-for-recursive-relations). |
| `@omit` on a unique constraint | Remove the generated `/{table}/by/{cols}/…` route (and its OpenAPI path); filtering is unaffected (the backing index remains). |

```sql
COMMENT ON CONSTRAINT posts_author_id_fkey ON posts IS E'@fieldName author\n@foreignFieldName posts';
```

turns `posts?select=*,users(name)` into `posts?select=*,author(name)`,
`users?select=*,posts(title)` stays `posts` (already the table name), and a
nested create becomes `POST /posts {"title": "…", "author": {"connect": {"id": 1}}}`.

### Functions

| Tag | Effect |
|---|---|
| `@omit` | Hide the function: no `/rpc/{fn}` route, or no computed column. |
| `@name <new_name>` | Rename the RPC path segment (`/rpc/new_name`) or the computed column key. |
| `@deprecated [reason]` | `deprecated: true` on the RPC operations, or on the computed column's OpenAPI property. |
| `@omit filter` / `@omit order` on a computed column | Keep the field in `select=`, drop its `advanced-filters` filter param / `order=` value. |
| `@filterable` (alias `@sortable`) | Opt the computed column in when `advanced-filters` runs in opt-in mode. |
| `@anyelement <type>` / `@anycompatible <type>` | Resolve a polymorphic function: its `anyelement` family (`anyarray`, `anyrange`, … derived from it) or `anycompatible` family takes `<type>` (`@anyelement integer`, `@anycompatible numeric`, `@anyelement public.mood`), so it becomes callable; without it the function is skipped. See [rpc.md](rpc.md#polymorphic-functions). |

### Enums (`COMMENT ON TYPE`)

| Tag | Effect |
|---|---|
| `@name <new_name>` | Rename the OpenAPI enum schema (`components.schemas.<new_name>`). Values are always the verbatim pg labels. |

### Contracts

`@contract a, b` and `@behavior ±contract:<name>` on tables, columns,
functions and foreign keys select which [schema contracts](contracts.md)
an object belongs to. They have no effect unless `contracts` is configured.

## Enum tables

`@enum` on a table turns its rows into an enum — the PostGraphile pattern for
lookup tables that are really a fixed vocabulary with referential integrity:

```sql
CREATE TABLE event_type (
    code        text PRIMARY KEY,
    description text
);
COMMENT ON TABLE event_type IS E'@enum\nKind of event.';

INSERT INTO event_type (code, description) VALUES
    ('conference', 'A large formal gathering'),
    ('meetup',     'Casual get-together');

CREATE TABLE events (
    id         integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       text NOT NULL,
    type_code  text NOT NULL REFERENCES event_type (code)
);
```

generates, in `openapi.json`:

```json
"event_type": {
  "type": "string",
  "description": "Kind of event.\n\nconference: A large formal gathering\nmeetup: Casual get-together",
  "enum": ["conference", "meetup"]
},
"events": {
  "type": "object",
  "properties": {
    "id":        {"type": "integer", "format": "int32", "readOnly": true},
    "name":      {"type": "string"},
    "type_code": {"$ref": "#/components/schemas/event_type"}
  }
}
```

and `GET /api/events?type_code=eq.meetup`, `POST /api/events {"name": "…",
"type_code": "meetup"}` validate against the labels.

- The **value column** is the table's single-column primary key, or the column
  of a single-column unique constraint commented `@enum`. It must be a
  text-family column — the row values become the enum values **verbatim**
  (`meetup` stays `meetup`; there is no case mapping in pdbr).
- **Descriptions** come from a column commented `@enumDescription`, falling
  back to a column named `description`.
- The enum table itself **disappears from the API** (no routes, no schema),
  and every single-column foreign key referencing the value column types its
  local column with the enum schema; those FKs generate no embed keys or
  relation filters.
- `@name` on the table renames the enum schema.
- Tables that do not qualify (multi-column or non-text key, or no rows) are
  left as ordinary resources.
- OpenAPI 3.1 has no per-value description slot, so the value descriptions
  are folded into the schema `description` as `value: description` lines
  after the type comment (as shown above); values without a description are
  left out of the list.

Rows are read **at introspection time** and values are ordered by the value
column; adding a row is DDL-adjacent — watch mode (or a restart) picks it up
like any other schema change. Like `@costMultiplier`, the tag is applied
during introspection (`pdbcore/introspect`) and therefore works even with the
`smart-comments` plugin disabled.

## Tuning advanced-filters per object

The relation/computed filter tags above are parsed by `advanced-filters`
directly from the catalog, so they work even if `smart-comments` is disabled.
Beyond the default opt-out (`@omit filter`/`@omit order`), the whole surface
can be flipped to explicit opt-in:

```yaml
plugins:
  settings:
    advanced-filters:
      relations_opt_in: true   # only FKs tagged @filterable get relation filters
      computed_opt_in: true    # only functions tagged @filterable get filter/order
```

## Cost estimation for recursive relations

The compiler rejects a request whose estimated cost exceeds
`server.max_cost` (default 100000) with `400 cost_exceeded`. The estimate is
`1 + Σ(mult × child)` over the embed tree, where `mult` is the embed's
explicit `limit` clamped to `server.max_page_size`, or `server.max_page_size`
(**100**) for a backward embed without one — the rows its SQL can fetch. That is right for an unrelated fan-out, but badly
pessimistic for self-referential trees — a Reddit-style comment thread —
where the fan-out at each level is far below the page.

A four-level thread,

```
GET /api/comments?select=*,comments(*,comments(*,comments(*)))
```

estimates `1 + 100·(1 + 100·(1 + 100·(1 + 100))) = 101 010 101`, far over
the default limit, even though the request returns a handful of rows in
practice.

`@costMultiplier <n>` on the recursive foreign key declares the *expected*
rows per parent, and the estimator uses it in place of the page size:

```sql
CREATE TABLE comments (
    id         integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    post_id    integer NOT NULL REFERENCES posts (id),
    parent_id  integer REFERENCES comments (id),
    body       text NOT NULL
);

COMMENT ON CONSTRAINT comments_parent_id_fkey ON comments IS '@costMultiplier 3';
```

The same request now estimates `1 + 3·(1 + 3·(1 + 3·(1 + 3))) = 121`.

Details worth knowing:

- **It only ever lowers an estimate.** A request whose explicit `limit` is
  smaller than the declared multiplier is charged the smaller `limit`, so the
  tag can never make a cheap request look expensive.
- **It applies to the backward side**, the direction that multiplies.
- **Values below 1 and unparseable values are ignored**, since a zero or
  negative multiplier would let a subtree escape the cost limit entirely.
- **It is parsed straight from the catalog**, so it works with the
  `smart-comments` plugin disabled.
- **It changes the estimate, not the query.** Rows actually returned are still
  bounded by `limit` and `server.max_page_size`. Setting the multiplier far
  below real fan-out weakens the cost limit as a guard — pick a value near
  the true average, not the smallest one that makes a request pass.

## Descriptions and OpenAPI

The description part of every comment (tags stripped) flows into
`openapi.json`:

| Comment on | Lands in |
|---|---|
| table / view | `components.schemas.<name>.description` and the `description` of each operation on its paths (the `summary` stays generated: `List users`, `Get one users`, …) |
| column | the property's `description` (and its filter parameter's, once query parameters are rendered — M6) |
| function | the RPC operations' `description`, or the computed column property (prefixed to the generated `Computed (schema.fn). Select explicitly: …` text) |
| enum type / `@enum` table | the enum schema's `description` |
| constraint | nothing (constraint comments are tag-only) |

Stripping is done by the core builder (`pdbcore/smarttags.Parse`) for every
comment it copies, so tag lines never reach OpenAPI — with or without the
plugin.

## What survives without the plugin

`plugins.disabled: [smart-comments]` removes route/method/parameter edits,
`@name`/`@fieldName` renames, `@notNull`/`@nullable`, `@deprecated`,
`@filterable`, and logical `@primaryKey`/`@unique`/`@foreignKey`. Still in
effect:

- `@enum` / `@enumDescription` and `@costMultiplier` — applied by
  `pdbcore/introspect` / the core builder before any plugin runs.
- Description stripping — the builder always drops tag lines from the
  descriptions it copies into OpenAPI.
- `@omit filter` / `@omit order` / `@filterable` on FKs and computed columns
  — read from the catalog by `advanced-filters` itself.
- `@omit filter` / `@omit order` on spatial columns — read by the `postgis`
  plugin itself (see [postgis.md](postgis.md)).

## Examples

```sql
-- A view made first-class: identified, relatable, read-only.
--   GET /order_totals/{order_id}, orders?select=*,order_totals(*)
COMMENT ON VIEW app.order_totals IS E'@primaryKey order_id\n@foreignKey (order_id) references app.orders (id)';

-- Hide credentials, rename a legacy column, phase out another.
COMMENT ON COLUMN app.users.hashed_password IS '@omit';
COMMENT ON COLUMN app.users.fullname IS '@name display_name';
COMMENT ON COLUMN app.users.balance IS '@deprecated use credits';

-- Sharpen relation naming: posts?select=*,author(name)
COMMENT ON CONSTRAINT posts_author_id_fkey ON app.posts IS E'@fieldName author\n@foreignFieldName posts';

-- Append-only audit log: POST works, nothing else.
COMMENT ON TABLE app.audit_log IS E'@omit read, update, delete\nImmutable audit trail.';

-- A search function that should not become a computed filter.
COMMENT ON FUNCTION app.users_score(app.users) IS '@omit filter,order';
```

## Implementation

`internal/plugins/smartcomments` (`New(settings) *Plugin`, no settings)
embeds `pdbcore/smartcomments/catalog.Hook` (the CatalogHook and the tag
`Index`) and adds:

- **InflectionHook** — `resource_path`, `field_name`, `rpc_name`,
  `computed_field` get the `@name` value substituted into the `Input` and
  then continue down the chain; `relation_forward` / `relation_backward`
  return `@fieldName` / `@foreignFieldName` verbatim when present, otherwise
  substitute the renamed table (and columns) and continue. FK tags are looked
  up under the referencing table's schema, falling back to a unique
  constraint-name match for cross-schema forward relations.
- **APIHook** — `api.Builder` mutators: `RemoveMethod` (`@omit
  read|create|update|delete`), `Resource.OmitList` (`@omit all`),
  `RemoveRelation` (`@omit many` on tables and FKs), `RemoveField`
  (column `@omit` / `@omit read`), `Resource.NoCreate` / `NoUpdate` (column
  `@omit create|update`, honoured by `Resource.InputColumnsFor` in the body
  parser), `RemoveFilterColumn` / `RemoveOrderColumn` (`@omit filter|order`
  on tables and columns), `Relation.Filterable` / `Orderable` (`@omit
  filter|order` on FKs), `Deprecated` + `DeprecationReason` on resources,
  fields, computed columns and RPCs, `RenameEnum` (`@name` on a type or
  `@enum` table).

## Related

- [rls.md](rls.md) — privileges remove methods the same way `@omit create|update|delete` does.
- [postgis.md](postgis.md) — `@omit filter,order` on spatial columns.
- [plugins.md](plugins.md) — the three hooks the plugin implements.
