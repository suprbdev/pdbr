# OpenAPI

pdbr describes the whole generated surface as an **OpenAPI 3.1** document.
It is served at `GET {base}/openapi.json` (`server.openapi`, on by default)
and printed by `pdbr schema print` (`--yaml` for YAML), which needs no
database when combined with `--schema.cache_path`.

pdbr ships **no UI, no CDN assets and no embedded documentation viewer** —
the document is the deliverable; bring your own tooling (see
[Consuming the document](#consuming-the-document)).

## Properties

- **Pure function of `api.Built`** — i.e. of catalog + config + enabled
  plugins + binary version. The schema cache stores the catalog, so booting
  from cache produces the same document as introspecting the same database
  ([caching.md](caching.md)).
- **Deterministic**: object keys, paths, parameters, enum values and tags
  are emitted in a stable order; two runs on identical inputs are
  byte-identical, so the document can be committed and diffed (golden tests
  do exactly that). Only `info.version` changes between binary versions.
- **Unauthenticated**: the endpoint ignores credentials and describes the
  full surface the connection role can see, not the caller's RLS view. In
  production, weigh `server.openapi: false` + shipping the document
  out-of-band ([deployment.md](deployment.md#production-checklist)).
- **Rebuilt on hot swap**: watch mode regenerates it with the routes.

## What is generated

### Top level

| Field | Value |
|---|---|
| `openapi` | `"3.1.0"` |
| `info.title` | `pdbr` |
| `info.version` | binary version (`-ldflags -X main.version`; `dev` when unset) |
| `servers` | `[{"url": "<server.base_path>"}]` (`[{"url": "/"}]` when empty) — relative, so it works behind any host/proxy |
| `tags` | one per resource, named after the resource key (`users`, `audit.users`) with the table comment as description, plus `rpc` (`<schema>.rpc` outside `schema.schemas[0]`) for functions; resources first in path order, so a single-schema API groups by table (`comments`, `events`, `places`, ..., `rpc`) instead of one `public` bucket |
| `components.securitySchemes.bearerAuth` | `{"type":"http","scheme":"bearer","bearerFormat":"JWT"}` — only when RLS is on and `rls.auth.mode: jwt` |
| `security` | `[{}, {"bearerAuth": []}]` in jwt mode: authentication is optional because anonymous requests are valid (they run as `rls.anonymous_role`) |
| `components.responses` | `Problem` (any error, `application/problem+json`) and `MethodNotAllowed` (same body plus the `Allow` header) — every operation's error responses `$ref` these |

`headers` auth mode emits no security scheme (the gateway is the
authenticator; an `apiKey`-in-header scheme per claim header would be noisy)
but, like jwt mode, documents `401` on every operation. With `rls.enabled:
false` credentials are ignored, so neither the scheme nor `401` appears.

### `components.schemas`

| Schema | When | Shape |
|---|---|---|
| `<name>` | every resource | object; one property per selectable column, computed column, pseudo-column (postgis accessors and the nullable `number` score columns) and relation key (relations reference the related resource schema — nullable object for forward, array for backward and for a forward `PERIOD` FK (PostgreSQL 18; the description says so, as it does for a `NOT ENFORCED` FK), `oneOf` when the key is ambiguous — and are only present when embedded via `select=`); `required` = non-nullable columns |
| `<name>.insert` | resource has `POST` | object; properties = `Resource.InputColumnsFor(POST)` (generated/identity columns and `@omit create` columns excluded); `required` = `NOT NULL` without default; `additionalProperties: false` (unknown keys are `400 invalid_body`) |
| `<name>.update` | resource has `PATCH` | object; properties = `InputColumnsFor(PATCH)`; no `required`, `minProperties: 1` (an empty patch is `400 invalid_body`); `additionalProperties: false` |
| `<name>.upsert` | resource has `PUT` | like `.insert` over `InputColumnsFor(PUT)`; the primary-key columns are added to `required` when they are all insertable (an identity `ALWAYS` key is not — the conflict target then comes from `?on_conflict=`, described on the parameter) |
| nested-input variants | `nested-mutations` plugin enabled | `.insert` and `.update` are extended **in place** (no separate schema): one property per key of `api.Surface.NestedKeys(resource)` that offers an operation there (`api.Surface.NestedSchema`), each an object with one property per operation, `minProperties: 1`. Forward: `create` (`$ref <related>.insert`), `connect` (`$ref <related>.connect`), and on `.update` `disconnect` / `delete` (`boolean`, `const: true`) and `update` (`$ref <related>.update`); `.insert` adds `maxProperties: 1` and the FK columns of a forward key leave `required`. Backward: non-empty arrays — `create` of `<related>.insert`, `connect` of `<related>.connect`, and on `.update` `delete` / `disconnect` of `<related>.connect` and `update` of `{"where": <related>.connect, "data": <related>.update}`. Operations the relation does not offer (privileges, `@omit`, FK writability, hidden referenced columns, nullable FK for `disconnect`) are left out. `.update` also serves `PATCH` by filter, which rejects nested keys. Depth (`max_depth`) is enforced at runtime, not in the schema graph, which is naturally recursive |
| `<name>.connect` | `nested-mutations` plugin enabled, resource a nested key of a resource with `POST` or `PATCH` leads to | key of an existing row for `connect` / `update.where` / `delete` / `disconnect`: `oneOf` one object per key set (primary key, then each unique constraint, then a temporal key without its period, which only a forward `connect` over a `PERIOD` FK accepts), each with `required` = its columns (`internal/plugins/nestedmutations`) |
| enum types | each pg enum | `{"type":"string","enum":[labels verbatim]}` (table-backed `@enum` tables too; per-value descriptions folded into the description) |
| composite types | each pg composite | object with one property per attribute (nested composites/arrays reference their schemas) |
| `<name>.row` | resources whose row type some function takes as an argument | object; one optional, nullable property per readable column, `additionalProperties: false` — the value of a row-typed RPC argument ([rpc.md](rpc.md#table-valued-row-arguments)) |
| domains | each domain over a built-in type, while `server.openapi_domains` is on (default) | the base type's schema plus `pattern` / bounds derived from simple `CHECK`s, described by the domain's comment and constraints — see [Domains](#domains) |
| `GeoJSON` | `postgis` plugin enabled and a spatial column exists | GeoJSON geometry object (`type` + `coordinates`, or `GeometryCollection`); used by geometry/geography columns; request-body properties are `oneOf [GeoJSON, string]` (WKT/EWKT) via `Builder.Extra.InputTypes` |
| `Problem` | always | RFC 9457 problem details ([errors.md](errors.md)): `type`, `title`, `status` (required), `detail`, `instance`, `code`, and `pg` (object, present only under `errors.detail: dev`) |

`<name>` is the resource path key: `users` for a table in `schema.schemas[0]`,
`audit.users` for other schemas (matching the canonical `/audit/users`
route). Enum, composite and domain schemas follow the same rule (`mood`,
`audit.mood`); a name already taken gets a `_` suffix (with a warning).

Per-property details:

- `description` from the column/table/type comment with smart-comment tags
  stripped ([smart-comments.md](smart-comments.md)).
- `deprecated: true` and `x-deprecation-reason` from `@deprecated [reason]`
  (on the resource property, the input property and the filter parameter).
- `readOnly: true` for generated/identity columns, computed columns and
  pseudo-columns (they appear in the resource schema, never in the input
  schemas; sending a generated column → `400 column_readonly`).
- Nullable columns use a type array: `{"type": ["string", "null"]}`; for
  `$ref` properties (composites, enums, GeoJSON) nullability is expressed as
  `{"anyOf": [{"$ref": ...}, {"type": "null"}]}`.

### Type map

| PostgreSQL | OpenAPI |
|---|---|
| `int2`, `int4` | `integer`, `format: int32` |
| `int8` | `string` (see below) |
| `numeric`, `money` | `string` (see below) |
| `float4`, `float8` | `number` (`format: float` / `double`) |
| `bool` | `boolean` |
| `text`, `varchar`, `char`, `name`, `citext` | `string` |
| `uuid` | `string`, `format: uuid` |
| `timestamptz`, `timestamp` | `string`, `format: date-time` |
| `date` | `string`, `format: date` |
| `time`, `timetz` | `string`, `format: time` |
| `interval` | `string` |
| `bytea` | `string`, `contentEncoding: base64` |
| `json`, `jsonb` | `{}` (any value) |
| `hstore` | `object` (string → string\|null) |
| `vector`, `halfvec` (pgvector) | `array` of `number`; input: `oneOf [array of number, string]` (the text form) |
| `sparsevec` (pgvector) | `string` (`{1:1.5,3:2}/5`); input: `oneOf [string, array of number]` (dense) |
| `cube` | `oneOf [array of number (point), pair of arrays of number (box)]`; input adds the text form (`string`) |
| `earth` (earthdistance) | `object` `{lat, lon}` (numbers in degrees, both required) |
| `raster` (postgis_raster) | never in the read schema; input: `string` (hex WKB), write-only |
| ranges, multiranges | `string` (the literal); input: `oneOf [string, {start, end, startInclusive, endInclusive, isEmpty}]` (multiranges: `oneOf [string, array]`) |
| `inet`, `cidr`, `macaddr`, `ltree`, `tsvector` | `string` |
| arrays (`T[]`) | `array`, `items` = element mapping (nested arrays nest) |
| enum | `$ref` to the enum schema |
| composite | `$ref` to the composite schema |
| domain | `$ref` to the domain schema (an array of a domain: `array` of the `$ref`); with `server.openapi_domains` off, or for a domain over a range, PostGIS or extension type, its base type's mapping |
| `geometry`, `geography` (postgis plugin) | `$ref: #/components/schemas/GeoJSON` (input: `oneOf [GeoJSON, string]`) |
| anything else | `string` |

**Why `int8`/`numeric`/`money` are strings.** PostgreSQL builds the response
JSON (`json_build_object`), and pdbr serializes 64-bit integers and arbitrary-precision
numerics as JSON **strings** so no client loses precision (JavaScript numbers
are IEEE-754 doubles; many generators map `number` to `double`). This mirrors
pdbq's `BigInt`/`BigFloat` scalars. Request bodies and filter values accept
either a JSON number or a string for these types; the document declares
`string` because that is what responses carry.

### Domains

With `server.openapi_domains` on (the default), every domain (`CREATE
DOMAIN`) over a built-in type gets a component schema, and columns, array
columns (`email[]` → `array` of the `$ref`), request bodies, path keys and
RPC arguments and results reference it:

```sql
CREATE DOMAIN email AS text CHECK (VALUE ~ '^[^@]+@[^@]+$');
COMMENT ON DOMAIN email IS 'An email address.';
CREATE DOMAIN percent AS int CHECK (VALUE >= 0 AND VALUE <= 100);
```

```json
"email": {
  "type": "string",
  "pattern": "^[^@]+@[^@]+$",
  "description": "An email address.\n\nDomain public.email over text.\n\nConstraints: `CHECK ((VALUE ~ '^[^@]+@[^@]+$'::text))`."
},
"percent": {
  "type": "integer", "format": "int32",
  "description": "Domain public.percent over int4.\n\nConstraints: `CHECK (((VALUE >= 0) AND (VALUE <= 100)))`.",
  "minimum": 0, "maximum": 100
}
```

- The schema is the base type's mapping (an `int8` domain is a `string`,
  a domain over `int[]` an `array`).
- The description is the domain's comment (smart tags stripped), the base
  type, and every `NOT NULL`/`CHECK` constraint of the domain and of the
  domains it is declared over.
- Keywords come only from constraints that map trivially: `pattern` from
  `VALUE ~ '<regex>'` on a text-like base (only when there is exactly one
  such constraint; PostgreSQL's regex dialect is close to, not identical
  with, ECMA-262), and `minimum` / `exclusiveMinimum` / `maximum` /
  `exclusiveMaximum` from `VALUE >= n`, `> n`, `<= n`, `< n` with an
  integer `n` on an `int2`/`int4`/`float4`/`float8` base, also inside a
  top-level `AND`. Anything else (functions, `OR`, `~*`, `NOT VALID`
  constraints) is only listed in the description.
- Domains over ranges, PostGIS and extension types (`citext`, `hstore`,
  `ltree`, …) keep their base type's schemas, whose request and response
  shapes differ.
- Filters are unchanged: a domain column takes its base type's operators.

With `server.openapi_domains: false` every domain is inlined as its base
type's mapping.

### `paths`

For every resource (`{r}` = `/users` or `/audit/users`; resources in
`schema.schemas[0]` are documented under the bare alias only — or under the
canonical `/public/users` when the alias is suppressed by a reserved-name
collision — and under the canonical schema-qualified path for the rest).
Documenting one path per resource keeps the document half the size and
generators produce one client method per operation; the runtime still
answers both forms.

| Path | Operations | Notes |
|---|---|---|
| `{r}` | `GET`, `HEAD`, `POST`, `PUT`, `PATCH`, `DELETE` | `GET`/`HEAD`: list (`HEAD` mirrors `GET`'s parameters and response headers, no content). `POST`: body `<name>.insert` or a non-empty array of it (`oneOf`), `201` + `Location` header / `204` (single object skipped by `resolution=ignore-duplicates`), `Prefer` header param (`return=`, `handling=`, `resolution=`, `missing=`), `on_conflict` param when the resource has a key. `PUT`: body `<name>.upsert` or array, `200` (updated) / `201` (inserted, `Location`) / `204` (`return=minimal`), `on_conflict` param listing the unique column sets. `GET`/`HEAD` also document the `Prefer` (`count=`), `Range` and `Range-Unit` header params, the `Content-Range` / `Preference-Applied` response headers, the `206` partial-content response and the `416` `RangeNotSatisfiable` component (`Content-Range: */<total>`; `SETOF <table>` functions too). `PATCH`/`DELETE`: bulk write by filter — `select`, `Prefer`, the logic params and one filter param per filterable column (no paging/order params), `PATCH` body `<name>.update`, `200` array / `204` |
| `{r}/{pk}` | `GET`, `HEAD`, `PUT`, `PATCH`, `DELETE` | path param per PK column, named after the column (`/users/{id}`; composite `/memberships/{org_id},{user_id}`); `ETag` header + `304` + `If-None-Match` param on `GET` when `server.etags`; `If-Match` param + `412` on `PATCH`/`DELETE`; `PATCH` body `<name>.update`; `200` / `204` (`return=minimal`); `PUT` (writable keys only) body = the `.upsert` properties inline with the key columns optional, `200` / `201` + `Location` / `204` |
| `{r}/by/{cols}/{vals}` | `GET`, `HEAD`, `PUT`, `PATCH`, `DELETE` | one **concrete** path per unique constraint, columns in constraint order: `/users/by/email/{email}`, `/memberships/by/org_id,slug/{org_id},{slug}` — the runtime also accepts other column orders, the document lists one |
| `{base}/rpc/{fn}` (`/rpc/search_posts`, `/audit/rpc/fn`) | `GET`, `HEAD` (stable/immutable only) and `POST` | `GET`: one query param per argument (typed by the column map; composites/arrays/json/GeoJSON travel as `string` — JSON or WKT text; `required` unless the argument has a `DEFAULT`, whose expression the description shows — an omitted arg takes it; a `VARIADIC` arg is an `array` param, `style: form`, `explode: true`); `POST`: body object with the same properties (a row-typed argument references `<name>.row`, an array of rows an array of it; `additionalProperties: false`, `required` = the arguments without a default; the body itself is `required` when that list is non-empty). Response per return type: scalar → the mapped type (nullable); `void` → `204`; row → nullable resource/composite schema (record of `OUT` params → the `rpc.<fn>` component schema, one property per output parameter, not nullable); `SETOF table` / `SETOF record` (`RETURNS TABLE`) → array + the list read params (`select`, `order`, `limit`, `offset`, `count`, logic and filter params — filter params shadowed by an argument name are omitted: arguments win) + `X-Total-Count`/`Link` headers + `text/csv`; `SETOF scalar|composite` → array + `limit`/`offset` + `text/csv`; `rls.auth.jwt_type` return → `string`, `format: jwt` |

Methods a resource does not support (views, missing privileges, `@omit
create|update|delete`) are absent from the document, exactly as they are
absent at runtime (`405` + `Allow`). A table with `@omit all` keeps its
collection path for the write methods only. `server.read_only` is **not**
reflected in the document (the document is a function of `api.Built`;
read-only is a runtime toggle via `App.SetReadOnly` and answers `405` at
request time — the `405` response is documented on every write).
Write operations document the `Prefer` header (`return=`, `handling=`, and
`max-affected=` on `PATCH`/`DELETE`) and a `Preference-Applied` response
header.

`operationId` scheme: `<name>.list|create|upsert|bulk_update|bulk_delete` on the collection,
`<name>.get|put|update|delete` on the primary-key path,
`<name>.by_<cols joined by _>[.put|.update|.delete]` on unique-constraint paths,
`rpc.<fn>` (`GET`) / `rpc.<fn>.post`; every `HEAD` operation is the `GET`
id plus `.head`. Multi-schema names carry the schema (`audit.log.list`).

**Query parameters** (all `in: query`; string-typed, so `style`/`explode`
stay at their defaults):

| Param | Where | Schema / description |
|---|---|---|
| `select` | every operation with a body response | `string`; grammar summarised in the description, columns, computed and derived fields and embeddable relation keys listed (with the per-embed `rel.order=`/`rel.limit=`/`rel.offset=`/`rel.<col>=` forms and `!inner`); projection args `col(transform=,simplify=,buffer=,precision=)` mentioned when the postgis plugin enables them; on list `GET`s (and `SETOF <table>` RPCs) the aggregate grammar when `server.aggregates` is on, with the spatial aggregates where the resource has a spatial column |
| `order` | list, `SETOF` rpc | `string`; orderable columns listed; computed / related (`rel(col)`) / distance (`<col>.dist`, needs `near=`) forms mentioned only when the corresponding plugin surface is on; full-text relevance (`<col>.rank`, `<col>.rank_cd`), trigram distance (`<col>.trgm_dist`, … needs `trgm=`) and vector distance (`<col>.vec_l2`, … needs `vec=`) terms listed where the column admits them, pgvector columns themselves not listed as plainly orderable ([contrib.md](contrib.md#ordering)) |
| `limit`, `offset` | list, `SETOF` rpc | `integer`, `limit` `minimum: 1`, `maximum: server.max_page_size`, `offset` `minimum: 0` |
| `after`, `before` | list (resources with a PK) | `string` (opaque cursor) |
| `count` | list, `SETOF <table>` rpc | `boolean` |
| `distinct` | list | `string` (comma list) |
| `on_conflict` | `PUT` | `string` (comma list of a unique constraint's columns; the description lists the resource's unique column sets) |
| `and`, `or`, `not.and`, `not.or` | list, `SETOF <table>` rpc | `string`; nested logic grammar |
| `{column}` | list, `SETOF <table>` rpc — one per **filterable** column under the active policy (`filters.indexed_only`, `allow_columns`, `@omit filter`), catalog order | `string`; description lists the operator vocabulary for that column's type (scalar `eq neq gt gte lt lte in is isdistinct`; text adds `like ilike sw ew`; arrays `eq…lte is isdistinct cs cd ov`; `jsonb` `eq…lte is isdistinct cs cd hk jpe jpm`; `json` `is cs cd hk jpe jpm`; hstore `eq neq in is isdistinct`; pgvector, cube and earth types `is`; raster `intersects is` (`is` without the postgis plugin or with its `filters` off); then the contrib operators the filter policy admits for the column — full-text, trigram, unaccent, hstore, ltree, range, inet, see [contrib.md](contrib.md); spatial types take `Built.Extra.FilterOps[type]` — the postgis `st_*`/`bbox_*`/`is` set, or just `is` without the plugin) and mentions the `not.` prefix once; vendor extension `x-pdbr-filter-ops: ["eq","neq",...]` carries the same list machine-readably (`internal/parse` has a test pinning it to what the parser accepts). `deprecated`/`x-deprecation-reason` from the column |
| `near` | list of resources with a `.dist`-orderable spatial column (postgis plugin, `Surface.SpatialOrder`), a `<col>_distance` column or a raster `<col>_value` | `string` (GeoJSON or WKT) |
| `trgm` | list of resources with a column orderable by trigram distance | `string` (reference text for `<col>.trgm_dist` and the word forms) |
| `vec` | list of resources with a column orderable by vector or cube distance, a vector or cube score or a `vec_*_lt` threshold | `string` (reference vector for `<col>.vec_*`: JSON array of numbers, for cube a pair of corner arrays, or the type's text form) |
| `latlon` | list of resources with an earthdistance order term, score or `earth_lt` / `earth_miles_lt` threshold | `string` (`<lat>,<lon>` in degrees) |
| `{arg}` | rpc `GET` | mapped type of the argument; `required` unless it has a `DEFAULT` (shown in the description) |

Per-embed params (`posts.order=`, `posts.limit=`, `posts.title=eq.x`) are
described in the `select` description; relation and computed-column filter
forms (advanced-filters plugin: `rel.col=`, `rel!none.`, `rel!every.`,
`rel=exists.`, `computed=op.v`) are described in prose appended to the list
operation's `description` (after the table comment) — only for relations /
computed columns that are filterable under `Built.Surface` and the plugin's
`Filterable` flags — rather than enumerated: the cross-product is unbounded.

**Header parameters**: `Prefer` (`return=representation|minimal`, enum) on
`POST`/`PUT`/`PATCH`/`DELETE`; `If-None-Match` on single-row `GET`/`HEAD` and
`If-Match` on `PATCH`/`DELETE` (only when `server.etags`); `Accept`
(`application/json` | `application/geo+json` | `text/csv`, enum; the geo type only on resources with a spatial column) on list `GET`/`HEAD` and set-returning
rpc `GET`/`HEAD` when `server.csv` (`text/csv`), and on key lookups and single-row rpc `GET`s over a spatial resource (geo+json only).

**Responses**: success responses carry `application/json` (`$ref` to the
resource schema, or an array of it; `oneOf` object|array on `POST`/`PUT`) and,
for lists and set-returning rpc `GET`s under `server.csv`, `text/csv`, and
`application/geo+json` (`GeoJSONFeatureCollection` / `GeoJSONFeature`, postgis
plugin) on lists, key lookups, `SETOF <table>` and single-row rpc `GET`s of a
resource with a spatial column. Response
headers are documented where they occur: `X-Total-Count`, `Link` on lists
and `SETOF <table>` rpc, `X-Next-Cursor`/`X-Prev-Cursor` on keyset-capable
lists; `ETag` on single-row `200`; `Location` on `201`; `Allow` on `405`
(the `MethodNotAllowed` component response). Error responses reference
`Problem` under `application/problem+json`, one entry per status the
operation can produce plus `default`: reads `400 401* 403 406 500 503 504`,
keyed reads add `404` (and `304` as a success), creates/upserts add
`405 409 413 415`, updates add `404 405 409 412 413 415`, deletes `404 405
409 412`, volatile rpc `POST` adds `405 413 415` (`401` only when auth is on).

### Plugins

Plugins extend the document through the `APIHook`
(`TransformAPI(ctx, *api.Builder)`) — the same seam that adds their runtime
surface, so document and behaviour cannot disagree ([plugins.md](plugins.md)).
The core generator renders everything from the model (`Built.Surface`,
`Resource.Pseudo`, `Extra.Schemas/Types/InputTypes/FilterOps`,
`Relation/Computed.Filterable/Orderable`, `Resource.InputColumnsFor`) and
never names a plugin:

| Plugin | Adds |
|---|---|
| `smart-comments` | removes routes/methods/params/properties per `@omit`, sets `deprecated`/`x-deprecation-reason`, strips tags from descriptions, `@omit create|update` shape the `.insert`/`.update`/`.upsert` properties |
| `advanced-filters` | the relation-filter (`rel.col=`, `rel!none.`, `rel!every.`, `rel=exists.`), computed-column filter and related/computed `order=` prose |
| `postgis` | `GeoJSON` schema, `oneOf [GeoJSON, string]` inputs, spatial ops in `x-pdbr-filter-ops`, `near` param, `<col>.dist` in `order`, `<col>_area`-style pseudo-columns, projection args in the `select` description |
| `nested-mutations` | `<name>.connect` schemas; nested-input properties on `.insert` and `.update` (`Surface.NestedKeys` + `Surface.NestedSchema`) |

Disabling a plugin removes exactly its surface (`test/e2e_openapi_test.go`
checks each one), and the document validates against the OpenAPI 3.1
meta-schema in the e2e suite (`github.com/santhosh-tekuri/jsonschema/v6`,
test-only; the schema JSON is vendored under `test/testdata`).

## Example excerpt

For the demo `users` table (JSON, abbreviated; the real thing is the golden
`internal/api/testdata/openapi.json`):

```json
{
  "openapi": "3.1.0",
  "info": {"title": "pdbr", "version": "0.1.0"},
  "servers": [{"url": "/api"}],
  "tags": [{"name": "comments"}, {"name": "events"}, {"name": "places"}, {"name": "posts"}, {"name": "users"}, {"name": "rpc"}],
  "paths": {
    "/users": {
      "get": {
        "tags": ["users"],
        "summary": "List users",
        "operationId": "users.list",
        "parameters": [
          {"name": "select", "in": "query", "schema": {"type": "string"},
           "description": "Columns and embeds to return. Comma list of items; item = column | alias:column | embed | *. Columns: id, email, full_name, mood, ... Computed (select explicitly): post_count. Embeds: places, posts (...)."},
          {"name": "order", "in": "query", "schema": {"type": "string"}, "description": "Comma list of <col>[.asc|.desc][.nullsfirst|.nullslast] ... Orderable columns: id, email, mood, ..."},
          {"name": "limit", "in": "query", "schema": {"type": "integer", "minimum": 1, "maximum": 100}},
          {"name": "offset", "in": "query", "schema": {"type": "integer", "minimum": 0}},
          {"name": "after", "in": "query", "schema": {"type": "string"}},
          {"name": "before", "in": "query", "schema": {"type": "string"}},
          {"name": "count", "in": "query", "schema": {"type": "boolean"}},
          {"name": "distinct", "in": "query", "schema": {"type": "string"}},
          {"name": "and", "in": "query", "schema": {"type": "string"}}, {"name": "or", "...": "..."},
          {"name": "id", "in": "query", "schema": {"type": "string"},
           "description": "Filter: id=<op>.<value> (int4). Ops: eq, neq, gt, gte, lt, lte, in, is, isdistinct. Prefix not. to negate (id=not.eq.<value>).",
           "x-pdbr-filter-ops": ["eq", "neq", "gt", "gte", "lt", "lte", "in", "is", "isdistinct"]},
          {"name": "email", "in": "query", "schema": {"type": "string"},
           "x-pdbr-filter-ops": ["eq", "neq", "gt", "gte", "lt", "lte", "in", "is", "isdistinct", "like", "ilike", "sw", "ew"]},
          {"name": "Accept", "in": "header", "schema": {"type": "string", "enum": ["application/json", "text/csv"]}}
        ],
        "responses": {
          "200": {
            "description": "OK",
            "headers": {
              "X-Total-Count": {"schema": {"type": "integer"}, "description": "Total rows after filters; present with count=true or Prefer: count=."},
              "X-Next-Cursor": {"schema": {"type": "string"}},
              "X-Prev-Cursor": {"schema": {"type": "string"}},
              "Link": {"schema": {"type": "string"}}
            },
            "content": {
              "application/json": {"schema": {"type": "array", "items": {"$ref": "#/components/schemas/users"}}},
              "text/csv": {"schema": {"type": "string"}}
            }
          },
          "400": {"$ref": "#/components/responses/Problem"},
          "default": {"$ref": "#/components/responses/Problem"}
        }
      },
      "post": {
        "operationId": "users.create",
        "parameters": [{"name": "select", "in": "query", "...": "..."}, {"name": "Prefer", "in": "header", "schema": {"type": "string", "enum": ["return=representation", "return=minimal"]}}],
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"oneOf": [
          {"$ref": "#/components/schemas/users.insert"},
          {"type": "array", "items": {"$ref": "#/components/schemas/users.insert"}, "minItems": 1}]}}}},
        "responses": {
          "201": {"description": "Created (Prefer: return=minimal omits the body).", "headers": {"Location": {"schema": {"type": "string"}}},
                  "content": {"application/json": {"schema": {"oneOf": [{"$ref": "#/components/schemas/users"}, {"type": "array", "items": {"$ref": "#/components/schemas/users"}}]}}}},
          "405": {"$ref": "#/components/responses/MethodNotAllowed"},
          "409": {"$ref": "#/components/responses/Problem"},
          "default": {"$ref": "#/components/responses/Problem"}
        }
      }
    },
    "/users/{id}": {
      "parameters": [{"name": "id", "in": "path", "required": true, "schema": {"type": "integer", "format": "int32"}}],
      "get": {"operationId": "users.get", "parameters": [{"name": "select", "...": "..."}, {"name": "If-None-Match", "in": "header", "...": "..."}],
              "responses": {"200": {"headers": {"ETag": {"schema": {"type": "string"}}}, "...": "..."}, "304": {"description": "Not Modified (If-None-Match matched)."}, "404": {"$ref": "#/components/responses/Problem"}}},
      "patch": {"operationId": "users.update", "requestBody": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/users.update"}}}}, "...": "..."}
    },
    "/users/by/email/{email}": {"get": {"operationId": "users.by_email", "...": "..."}},
    "/rpc/search_posts": {"get": {"operationId": "rpc.search_posts", "parameters": [{"name": "term", "in": "query", "required": true, "schema": {"type": "string"}}, {"name": "select", "...": "..."}], "...": "..."},
                          "post": {"operationId": "rpc.search_posts.post", "requestBody": {"required": true, "content": {"application/json": {"schema": {"type": "object", "properties": {"term": {"type": ["string", "null"]}}, "required": ["term"], "additionalProperties": false}}}}}}
  },
  "components": {
    "schemas": {
      "users": {
        "type": "object",
        "required": ["balance", "created_at", "email", "id"],
        "properties": {
          "id": {"type": "integer", "format": "int32", "readOnly": true},
          "email": {"type": "string"},
          "full_name": {"type": ["string", "null"]},
          "mood": {"anyOf": [{"$ref": "#/components/schemas/mood"}, {"type": "null"}]},
          "settings": {},
          "tags": {"type": ["array", "null"], "items": {"type": "string"}},
          "balance": {"type": "string", "description": "int8, serialized as a string to preserve precision."},
          "created_at": {"type": "string", "format": "date-time"},
          "address": {"anyOf": [{"$ref": "#/components/schemas/address"}, {"type": "null"}]},
          "post_count": {"type": ["string", "null"], "readOnly": true, "description": "Computed (public.users_post_count). Select explicitly: select=post_count."},
          "posts": {"type": "array", "items": {"$ref": "#/components/schemas/posts"}, "description": "Embed via select=posts(...)."}
        }
      },
      "users.insert": {
        "type": "object",
        "description": "Create body for users: one object, or an array of objects for a bulk insert (missing keys take their column default).",
        "required": ["email"],
        "properties": {"email": {"type": "string"}, "full_name": {"type": ["string", "null"]}, "mood": {"...": "..."}, "balance": {"type": "string"},
                       "posts": {"type": "object", "required": ["create"], "properties": {"create": {"type": "array", "items": {"$ref": "#/components/schemas/posts.insert"}, "minItems": 1}}, "additionalProperties": false}},
        "additionalProperties": false
      },
      "users.update": {"type": "object", "minProperties": 1, "properties": {"...": "..."}, "additionalProperties": false},
      "users.upsert": {"type": "object", "required": ["email"], "properties": {"...": "..."}, "additionalProperties": false},
      "users.connect": {"oneOf": [{"type": "object", "required": ["id"], "...": "..."}, {"type": "object", "required": ["email"], "...": "..."}]},
      "mood": {"type": "string", "enum": ["sad", "ok", "happy"]},
      "address": {"type": "object", "properties": {"street": {"type": ["string", "null"]}, "city": {"type": ["string", "null"]}, "mood": {"...": "..."}}},
      "Problem": {"type": "object", "required": ["type", "title", "status"], "properties": {"type": {"type": "string"}, "title": {"type": "string"}, "status": {"type": "integer"}, "detail": {"type": "string"}, "instance": {"type": "string"}, "code": {"type": "string"}, "pg": {"type": "object"}}}
    },
    "responses": {
      "Problem": {"description": "Error ...", "content": {"application/problem+json": {"schema": {"$ref": "#/components/schemas/Problem"}}}},
      "MethodNotAllowed": {"description": "Method Not Allowed ...", "headers": {"Allow": {"schema": {"type": "string"}}}, "content": {"application/problem+json": {"schema": {"$ref": "#/components/schemas/Problem"}}}}
    },
    "securitySchemes": {"bearerAuth": {"type": "http", "scheme": "bearer", "bearerFormat": "JWT"}}
  },
  "security": [{}, {"bearerAuth": []}]
}
```

## Consuming the document

Fetch it live or generate it at build time:

```console
$ curl -s https://api.example.com/api/openapi.json > openapi.json
$ pdbr schema print --schema.cache_path schema.cache > openapi.json   # no DB needed
$ pdbr schema print --yaml > openapi.yaml
```

Then use whichever tool you already use — nothing pdbr-specific is required.
Two things to know for every tool:

- The document is **3.1** (nullable = type arrays, `contentEncoding`).
  Tools that only understand 3.0 need a down-conversion step, e.g.
  `npx @apiture/openapi-down-convert --input openapi.json --output openapi-3.0.json`.
- `int8`/`numeric`/`money` are `string`; generated clients get strings for
  those fields by design (see [Type map](#type-map)). `x-pdbr-filter-ops` is
  ignored by generators, harmlessly.

| Tool | Typical use |
|---|---|
| **openapi-generator** | `openapi-generator-cli generate -i openapi.json -g typescript-fetch -o client/` (also `go`, `python`, `kotlin`, `rust`, ...). Prefer generator versions with 3.1 support; otherwise down-convert first |
| **oapi-codegen** (Go) | `oapi-codegen -generate types,client -package api openapi.json > api.gen.go`; the per-column filter params become optional string fields on the params struct |
| **Redocly CLI** | `redocly lint openapi.json`, `redocly preview-docs openapi.json`, `redocly build-docs openapi.json -o docs.html` (single static HTML you host anywhere) |
| **Stoplight Elements / Prism** | render docs from the URL, or `prism mock openapi.json` for a mock server matching the contract |
| **Bruno / Insomnia / Postman** | import `openapi.json` (or the live URL) to get a collection with one request per operation; set the bearer token once at collection level |
| **Schemathesis / Dredd** | contract/fuzz testing against a running pdbr |

Because the document is deterministic and version-controllable, a common
setup is: `pdbr schema dump` + `pdbr schema print` in CI, commit both,
`redocly lint` + client generation as later CI steps
([caching.md](caching.md#ci-drift-gate)).

## Exposing your own UI

pdbr will not serve Swagger UI, Redoc, Scalar or any other viewer, by design.
If you want interactive docs:

- **Static page + live document.** Host a Swagger UI / Redoc / Scalar /
  Elements page anywhere (your reverse proxy, a static bucket, your frontend
  app) and point it at `https://api.example.com/api/openapi.json`. When the
  page's origin differs from the API's, add that origin to
  `server.cors_origins` so the browser can fetch the document and try
  requests.
- **Build-time bundle.** `pdbr schema print > openapi.json` in CI, then
  `redocly build-docs` (or the viewer's bundler) into a self-contained HTML
  you deploy with your docs site. No CORS involved; regenerate whenever the
  drift gate fires.
- **Same host via the proxy.** Serve the viewer at `/docs` from nginx/your
  gateway and proxy `/api/*` to pdbr; the document is then same-origin.
- **Embedded as a library.** `pdbr.New(cfg, ...)` and `App.Handler(ctx)`
  give you an `http.Handler`; mount it in your own mux next to whatever
  handler serves your UI.

Whatever you choose, keep `server.openapi` on only if the surface is meant
to be public; otherwise serve the built-time document from behind your own
authentication.
