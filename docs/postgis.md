# PostGIS

pdbr maps PostGIS `geometry` and `geography` columns to GeoJSON and generates
the spatial query surface on top of them: bounding-box search, DE-9IM
predicates, radius search, derived accessors, reprojection and distance
ordering — all compiled into the same **single SQL statement** as the rest of
the request.

Nothing needs to be configured. A column is spatial when its type name is
`geometry` or `geography`, whatever schema the extension was installed in. If
the database has a spatial column, the surface appears; if it does not, routes
and OpenAPI are byte-identical to what pdbr generates without PostGIS
installed.

## GeoJSON in and out

| Direction | Representation |
|---|---|
| output | a GeoJSON geometry object — `ST_AsGeoJSON(col)::jsonb` inside the response builder, never WKB hex |
| input | a GeoJSON object **or** a WKT/EWKT string |

```
GET /api/places?select=name,location
```
```json
[{"name": "Trafalgar Square",
  "location": {"type": "Point", "coordinates": [-0.1281, 51.508]}}]
```

Both input forms are accepted anywhere a spatial value is taken — filters,
`near=`, request bodies of `POST`/`PATCH`/`PUT`, RPC arguments:

```
POST /api/places
{"name": "Trafalgar Square",
 "location": {"type": "Point", "coordinates": [-0.1281, 51.508]}}

POST /api/places
{"name": "Elsewhere", "location": "SRID=4326;POINT(1 2)"}
```

GeoJSON `Feature` and `FeatureCollection` documents are unwrapped
automatically (a `FeatureCollection` becomes a `GeometryCollection`), so a
value pasted straight out of a map library works without editing.

**SRIDs.** Every spatial value — filter operand, `near=`, body value — is
parsed for the column it targets (`pdbcore/postgis.ParseFor` with the
column's typmod SRID), and the parameter alone carries the SRID. GeoJSON is
WGS84 (4326) per RFC 7946, which is what `ST_GeomFromGeoJSON` assigns; a
legacy `crs` member naming another SRID is kept in the parameter. On a
`geometry` column declared with an SRID (say `geometry(Point, 3857)`) the
GeoJSON branch is reprojected with `ST_Transform(…, 3857)` (a no-op when the
SRIDs already match). EWKT (`SRID=3857;POINT(...)`) carries its own SRID and
is passed through unchanged. Bare WKT is rewritten to EWKT with the column's
SRID (`POINT(1 2)` → `SRID=4326;POINT(1 2)` for `geometry(Point, 4326)`); on a
`geography` column it is tagged 4326, since geography requires a real CRS.

Bare WKT on an unconstrained `geometry` column keeps PostGIS's own default,
SRID 0. EWKT whose SRID differs from the column's is not reprojected: a
filter whose bounding boxes overlap fails with `ST_Intersects: Operation on
mixed SRID geometries` (`400 pg:XX000`, detail `invalid geometry` outside
`errors.detail: dev`) and a write with `22023` →
`400 pg:22023`. Type modifiers are enforced by PostgreSQL as well: a
`LineString` into `geometry(Point, 4326)` is `400 pg:22023`. Function
arguments carry no typmod, so RPC arguments follow the unconstrained rules.

**One parameter, one expression.** Every spatial value reaches SQL as a
single bind parameter inside one constructor expression whose text depends
only on the target column (flavour and catalog SRID), never on the value or
its input form:

```sql
CASE WHEN left($n, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($n), <srid>) ELSE ST_GeomFromEWKT($n) END
```

The `ST_Transform` appears only for a geometry column with a declared SRID,
and a geography target wraps the whole expression in `(…)::geography`.
GeoJSON, EWKT and bare WKT input therefore compile to the same SQL text (one
prepared statement per request shape); PostgreSQL folds the `CASE` at plan
time. Parsing, unwrapping and
constructor selection live in `pdbcore/postgis` (`ParseFor`, `Value.SQL`), shared
with pdbq. A value that is neither valid GeoJSON nor plausible WKT is rejected
with `400 invalid_filter` / `400 invalid_body` before any SQL is built.

## `Accept: application/geo+json`

`GET`/`HEAD` reads answer in GeoJSON (RFC 7946) when the client asks for
`application/geo+json` — a `FeatureCollection` on list routes and `SETOF
<table>` RPCs, a single `Feature` on key lookups (`/places/1`,
`/places/by/…`) and single-row RPCs. It is the PostgREST behaviour
(`ST_AsGeoJSON(record)`), and map clients (Leaflet, MapLibre) consume it
directly.

```
GET /api/places?select=id,name,location&limit=2
Accept: application/geo+json
```
```json
{"type": "FeatureCollection", "features": [
  {"type": "Feature",
   "geometry": {"type": "Point", "coordinates": [-0.1281, 51.508]},
   "properties": {"id": 1, "name": "Trafalgar Square"}}]}
```

- **Geometry.** The first plain spatial column of the selection, in
  `select=` order (the resource's column order for `*` or no `select=`),
  becomes the Feature `geometry`; `geometry=<key>` picks another one by its
  response key (alias or column name): `?select=id,location,area&geometry=area`.
  A key that is not a plain spatial select item is `400 invalid_select`.
  `geometry=` is rejected (`400 invalid_filter`) on any other media type, and
  is an ordinary filter parameter on a resource that has a column or computed
  column named `geometry`. Arrays, JSON paths, casts, computed columns and
  derived accessors are never the geometry. `transform=` / `simplify=` apply
  to the geometry item like anywhere else; a `NULL` geometry is
  `"geometry": null`.
- **Properties.** Every other select item, under its response key and in the
  JSON rendering of the normal response — further spatial columns stay GeoJSON
  objects, embeds (including spreads) are nested arrays/objects, 64-bit
  integers and `numeric` are strings. The geometry item is not repeated.
  A selection of only the geometry gives `"properties": {}`.
- **Everything else is the normal read.** `select=`, filters (spatial ones
  included), `order=`, `limit`/`offset`, keyset cursors, `count=` /
  `Prefer: count=`, `Range`, `Content-Range`, `X-Total-Count`, `Link`, `206`
  and `416`, `ETag` on key lookups, `404` for a missing key and RLS behave
  as for JSON; only the body and `Content-Type: application/geo+json` differ.
  An empty page is `{"type":"FeatureCollection","features":[]}`. The
  Features are built in the single SQL statement
  (`json_build_object('type','Feature','geometry',…,'properties',…)`, wrapped
  in a `FeatureCollection` around the aggregated page); the statement text
  depends only on the request shape.
- **`406 not_acceptable`** when the selection has no plain spatial column,
  the resource has no spatial column at all, the select has aggregate
  functions, the request is not a `GET`/`HEAD` (mutation responses are JSON
  only) or the route is a scalar/composite RPC. Media ranges rank by `q`
  like CSV: `application/geo+json;q=0.5, application/json` is JSON.

## Filtering

Spatial columns take **only** the operators below (plus `is.null` /
`not.is.null`); `eq`, `lt`, `in` and friends are not accepted (`400
invalid_filter`), because PostGIS types have no meaningful scalar ordering.
For the same reason spatial columns never appear in `distinct=` or in plain
`order=` (only `.dist`, below). Operators marked **index** can be answered by
a GiST index; the rest are exact but scan more rows, so pair them with a
bounding-box operator when the table is large.

| Operator | Value | SQL | Index |
|---|---|---|---|
| `bbox_intersects.(minx,miny,maxx,maxy[,srid])` | four numbers + optional SRID | `col && ST_MakeEnvelope($1,$2,$3,$4,<srid>)` | ✔ |
| `bbox_contains.(…)` | same | `col ~ …` | ✔ |
| `bbox_containedby.(…)` | same | `col @ …` | ✔ |
| `st_intersects.<geom>` | GeoJSON or WKT/EWKT | `ST_Intersects(col, <geom>)` | ✔ |
| `st_disjoint.<geom>` | geometry | `ST_Disjoint` | |
| `st_contains.<geom>` | geometry | `ST_Contains` | ✔ |
| `st_containsproperly.<geom>` | geometry | `ST_ContainsProperly` | ✔ |
| `st_within.<geom>` | geometry | `ST_Within` | ✔ |
| `st_covers.<geom>` | geometry | `ST_Covers` | ✔ |
| `st_coveredby.<geom>` | geometry | `ST_CoveredBy` | ✔ |
| `st_crosses.<geom>` | geometry | `ST_Crosses` | ✔ |
| `st_overlaps.<geom>` | geometry | `ST_Overlaps` | ✔ |
| `st_touches.<geom>` | geometry | `ST_Touches` | ✔ |
| `st_equals.<geom>` | geometry | `ST_Equals` | |
| `st_dwithin.(geom,dist[,spheroid])` | geometry, distance, optional `true`/`false` | `ST_DWithin(col, <geom>, $n)` | ✔ |
| `st_beyond.(geom,dist[,spheroid])` | same | `NOT ST_DWithin(...)` | |
| `bbox_leftof.<geom>` | geometry | `col << <geom>` (strictly left of) | ✔ |
| `bbox_rightof.<geom>` | geometry | `col >> <geom>` (strictly right of) | ✔ |
| `bbox_below.<geom>` | geometry | `col <<\| <geom>` (strictly below) | ✔ |
| `bbox_above.<geom>` | geometry | `col \|>> <geom>` (strictly above) | ✔ |
| `bbox_overlapsorleftof.<geom>` | geometry | `col &< <geom>` (does not extend to the right of) | ✔ |
| `bbox_overlapsorrightof.<geom>` | geometry | `col &> <geom>` (does not extend to the left of) | ✔ |
| `bbox_overlapsorbelow.<geom>` | geometry | `col &<\| <geom>` (does not extend above) | ✔ |
| `bbox_overlapsorabove.<geom>` | geometry | `col \|&> <geom>` (does not extend below) | ✔ |
| `bbox_equals.<geom>` | geometry | `col ~= <geom>` (same bounding box) | ✔ |
| `bbox_intersectsnd.<geom>` | geometry | `col &&& <geom>` (n-D boxes, Z/M included, overlap) | n-D |
| `bbox_intersects2d.<geom>` | geometry | `col && <geom>` (2D boxes overlap) | ✔ |
| `st_exactlyequals.<geom>` | geometry | `col = <geom>` (same coordinates, order and SRID) | B-tree |
| `st_orderingequals.<geom>` | geometry | `ST_OrderingEquals` (equal, same vertex order) | ✔ |
| `st_intersects3d.<geom>` | geometry | `ST_3DIntersects` | ✔ |

**Operator names.** Every spatial operator is spelled after its
`pdbcore/postgis` (and pdbq) name, lower-cased, with a `bbox_` prefix for
the bounding-box operators and `st_` for the rest: `bboxLeftOf` →
`bbox_leftof`, `bboxOverlapsOrBelow` → `bbox_overlapsorbelow`,
`exactlyEquals` → `st_exactlyequals`, `intersects3D` → `st_intersects3d`,
`bboxIntersects2D` → `bbox_intersects2d`. `bbox_intersects2d` is the same
`&&` as `bbox_intersects`, against a geometry operand's bounding box instead
of an envelope.
The names are part of the API and never change; new pdbcore operators are
added under the same rule.

**Bounding-box operand.** Only `bbox_intersects`, `bbox_contains` and
`bbox_containedby` take the `(minx,miny,maxx,maxy[,srid])` envelope (the
viewport case). The directional and equality bounding-box operators
(`bbox_leftof` … `bbox_equals`, `bbox_intersectsnd`, `bbox_intersects2d`) take a geometry, like
the `st_*` operators, and compare against that geometry's bounding box — a
geometry can carry the Z/M extent `&&&` needs where an envelope cannot.
"Left/right" compare X, "below/above" compare Y; `bbox_overlapsorleftof`
means "this box's right edge is not to the right of the operand's",
`bbox_overlapsorbelow` "this box's top edge is not above the operand's", and
so on, as in PostGIS.

**Index notes.** `&&&` (`bbox_intersectsnd`) is served by an n-D GiST index
(`USING gist (col gist_geometry_ops_nd)`), not the default 2D one;
`st_intersects3d` is prefiltered by the 2D index and pruned on Z by an n-D
one. `st_exactlyequals` (`=`) uses a B-tree index, not a GiST one.

All of them accept the `not.` prefix (`location=not.st_within.<geom>`) and
work inside `and=`/`or=` groups (`or=(location.st_intersects."…",…)`).
Geometries, bounding-box corners and distances are bind parameters; only the
SRID of a bounding box is a structural integer literal in the SQL text.

**Geography columns.** PostGIS defines only `&&` (`bbox_intersects`,
`bbox_intersects2d`), `=`, `ST_Intersects`, `ST_Covers`, `ST_CoveredBy` and
`ST_DWithin` for `geography`. The other
operators are still accepted on a geography column: pdbr casts both sides
to `geometry` (`ST_Within("area"::geometry, CASE … END)`,
`"area"::geometry ~ ST_MakeEnvelope(…)`, `"area"::geometry << …`), i.e. planar semantics in the
column's SRID, exactly as pdbq compiles them (`pdbcore/postgis.Op.Geography`
marks the native ones).

### URL encoding

The value after the operator is percent-decoded before parsing, so any WKT
or GeoJSON works once encoded (`curl --data-urlencode -G`, `URLSearchParams`,
etc.). A bare operator (`location=st_within.<geom>`) takes the rest of the
value verbatim — no quoting, so a GeoJSON object goes in as-is. Inside a
parenthesised list (`st_dwithin.(…)`, `bbox_*`, `and=(…)`/`or=(…)`), values
containing `,`, `(` or `)` — every GeoJSON object, WKT with more than one
point — must be double-quoted, and quotes inside the value escaped as `\"`
(the standard list quoting of [filtering.md](filtering.md)):

```
# EWKT, single operator
GET /api/places?location=st_intersects.SRID%3D4326%3BPOINT(-0.1281%2051.508)
GET /api/places?location=bbox_leftof.SRID%3D4326%3BPOINT(0%200)

# GeoJSON, single operator (encoded {"type":"Point","coordinates":[-0.1281,51.508]})
GET /api/places?location=st_within.%7B%22type%22%3A%22Polygon%22%2C%22coordinates%22%3A%5B%5B%5B-0.5%2C51.3%5D%2C%5B0.3%2C51.3%5D%2C%5B0.3%2C51.7%5D%2C%5B-0.5%2C51.7%5D%2C%5B-0.5%2C51.3%5D%5D%5D%7D

# list operators: quote the geometry
GET /api/places?location=st_dwithin.("SRID=4326;POINT(-0.1281 51.508)",5000,true)
GET /api/places?location=bbox_intersects.(-0.5,51.3,0.3,51.7)
GET /api/places?location=bbox_intersects.(-55000,6700000,33000,6750000,3857)

# GeoJSON inside a logic list: quoted, inner quotes escaped
GET /api/places?or=(location.st_within."{\"type\":\"Polygon\",\"coordinates\":[[…]]}",area.is.null)
```

(Spaces and quotes shown unencoded for readability; clients send `%20`,
`%22`.)

### Bounding box (viewport queries)

`bbox_*.(minx,miny,maxx,maxy[,srid])` — `srid` defaults to 4326 and is
inlined into `ST_MakeEnvelope`; the four corners are parameters. It names the
CRS the corners are expressed in and must match the column's SRID — PostGIS
does not reproject the box (the bounding-box operators on mixed SRIDs simply
never match), so a `3857` box is for data stored in `3857`. This is the cheap
first pass for "what is on screen":

```
GET /api/places?select=name&location=bbox_intersects.(-0.5,51.3,0.3,51.7)
```

### Radius search

`st_dwithin.(geom,dist[,spheroid])`.

**Units matter:** on a `geography` column the distance is **metres**; on a
`geometry` column it is in the column's **SRID units** (for EPSG:4326 that is
degrees, which is rarely what you want). Pass `true` as the third element to
measure a geometry column in metres — pdbr casts both sides to `geography`:

```
GET /api/places?select=name&location=st_dwithin.("POINT(-0.1281 51.508)",5000,true)
```

compiles the predicate as
`ST_DWithin("location"::geography, (CASE WHEN left($1, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($1), 4326) ELSE ST_GeomFromEWKT($1) END)::geography, $2)`
with `$1 = 'SRID=4326;POINT(-0.1281 51.508)'` (the bare-WKT operand is tagged
with the column's SRID, 4326).

## Derived accessors (pseudo-columns)

Each spatial column `<col>` gains sibling pseudo-columns computed in the same
statement — no extra round trip (the type-specific ones only where its type
modifier allows a non-null value, see below). They are selectable in `select=`, appear in
OpenAPI as `readOnly` properties, and cannot be written, filtered or ordered:

| Pseudo-column | JSON type | PostGIS |
|---|---|---|
| `<col>_area` | number | `ST_Area` (m² for geography, SRID units for geometry) |
| `<col>_length` | number | `ST_Length` |
| `<col>_perimeter` | number | `ST_Perimeter` (metres for geography, SRID units for geometry) |
| `<col>_centroid` | GeoJSON | `ST_Centroid` (spheroidal for geography) |
| `<col>_envelope` | GeoJSON | `ST_Envelope` |
| `<col>_srid` | integer | `ST_SRID` |
| `<col>_geometry_type` | string | `ST_GeometryType` (e.g. `ST_Point`) |
| `<col>_is_valid` | boolean | `ST_IsValid` |
| `<col>_x`, `<col>_y` | number | `ST_X`, `ST_Y` (longitude/latitude for geography); null unless the value is a point |
| `<col>_z`, `<col>_m` | number | `ST_Z`, `ST_M`; null unless the value is a point with that dimension |
| `<col>_num_points` | integer | `ST_NPoints` (vertex count) |
| `<col>_num_geometries` | integer | `ST_NumGeometries` (1 for a single geometry, 0 when empty) |
| `<col>_start_point`, `<col>_end_point` | GeoJSON | `ST_StartPoint`, `ST_EndPoint`; null unless the value is a LineString, CircularString or CompoundCurve |
| `<col>_point_on_surface` | GeoJSON | `ST_PointOnSurface` (a point guaranteed to lie on the geometry) |
| `<col>_wkt` | string | `ST_AsText` (WKT, no SRID) |
| `<col>_ewkt` | string | `ST_AsEWKT` (`SRID=4326;POINT(…)`) |
| `<col>_is_empty` | boolean | `ST_IsEmpty` |
| `<col>_is_simple` | boolean | `ST_IsSimple` |
| `<col>_xmin`, `<col>_ymin`, `<col>_xmax`, `<col>_ymax` | number | `ST_XMin` … `ST_YMax` (bounding-box extent; null when empty) |

```
GET /api/places?select=name,location_centroid,location_srid,area_area
GET /api/places?select=name,lon:location_x,lat:location_y,location_ewkt,area_xmin,area_ymax
```

Geometry-only functions are applied to a geography column through a
`::geometry` cast (`ST_NPoints("area"::geometry)`). `_area`, `_length` and
`_perimeter` use the native geography overloads and measure metres on the
spheroid; `_centroid` is the spheroidal `ST_Centroid(geography)`, except for a
GeometryCollection value, which PostGIS cannot centroid as geography and
which gets the planar centroid of the `::geometry` cast. The type-specific
accessors (`x`/`y`/`z`/`m`, `start_point`/`end_point`) render a `CASE` on
`ST_GeometryType`, so a value of another type yields `null` instead of a
PostGIS error.

**Per-column accessors.** An accessor that could only ever be `null` for a
column's type modifier is not generated for it: on `geometry(Point, 4326)`
there is `location_x`/`location_y` but no `location_z`, `location_m`,
`location_start_point` or `location_end_point` (`400 unknown_column`, absent
from OpenAPI); `geometry(PointZ, …)` adds `_z`, `geometry(PointM, …)` `_m`,
and `geometry(LineString, …)` has `_start_point`/`_end_point` but no `_x`.
Columns without a subtype (`geometry`, `geography`, `geometry(Geometry,
…)`), an unrecognised one, or a schema cache without typmod information get
every accessor. The decision is `pdbcore/postgis.Derived.AppliesTo` over
`introspect.Column.Spatial`.

A real column (or a computed column) that already owns one of these names
keeps it; the accessor is skipped with a build warning rather than shadowing
your data. Aliases work as for any field: `select=sq_m:area_area`. A NULL
geometry yields NULL accessors.

Pseudo-columns (like computed columns) are excluded from `*` and only
returned when named in `select=`, keeping default responses free of per-row
PostGIS function calls. They are select-only: naming one in a filter, in
`order=` or in a request body is `400 unknown_column` / `400 invalid_body`.
The plugin registers them as `api.Resource.Pseudo` entries
(`api.PseudoColumn{Key, Column, Accessor, Schema}`); the core compiler renders
`Accessor` through `pdbcore/postgis.Deriveds`.

## Reprojection, simplification, buffers and precision

Spatial fields and their pseudo-columns take parenthesised projection
arguments in `select=`: `buffer=<distance>`, `simplify=<tolerance>`,
`transform=<srid>` and `precision=<digits>`. The first three apply to the
column value before it is encoded as GeoJSON (or before the accessor runs:
`area_centroid(transform=3857)` is the centroid in web mercator,
`area_area(buffer=100)` the area of everything within 100 m of the
polygon). `precision` rounds the GeoJSON coordinates. Aliases let you fetch
several representations at once:

```
GET /api/places?select=name,web_mercator:location(transform=3857),coarse:location(simplify=0.01),short:location(precision=6),around:area(buffer=250,precision=6)
```

| argument | SQL | value |
|---|---|---|
| `buffer` | `ST_Buffer(…, $n::float8)`, the area within the distance | any finite number, a bind parameter: metres on `geography`, SRID units on `geometry` (degrees for 4326); negative distances shrink polygons and erase points and lines |
| `simplify` | `ST_SimplifyPreserveTopology(…, $n)`, never an invalid geometry | a non-negative tolerance, a bind parameter |
| `transform` | `ST_Transform(…, <srid>)` | a positive integer SRID, a structural literal |
| `precision` | `ST_AsGeoJSON(…, $n::int4)` (`maxdecimaldigits`) | an integer from 0 to 15, a bind parameter |

They apply in that order: buffer, simplify, transform, then the GeoJSON
encoding with the precision. A buffered `geography` stays `geography`
(metres), while simplify and transform work on `geometry`. Without
`precision` PostGIS writes up to 9 decimal digits; 6 digits are about 10 cm
on lon/lat data and make map payloads noticeably smaller. `precision`
applies only to GeoJSON values: a column, or a geometry-valued accessor
(`<col>_centroid`, `<col>_envelope`, …); on a number, text or boolean
accessor (`area_area`, `location_srid`, …) it is `400 invalid_select`. It
also rounds the `geometry` member of `Accept: application/geo+json`
responses when the Feature geometry item carries it
(`select=id,location(precision=6)`).

These are the only field arguments the `select=` grammar accepts on
spatial values (`400 invalid_select` on anything else, on a duplicate, on
empty parentheses, on a value out of range, or on a non-spatial field).
Each `buffer` costs `pdbcore/postgis.BufferCost` (5) per row in
`server.max_cost` on top of the column, because `ST_Buffer` builds a new
polygon for every row. A transformed value is no longer WGS84 even though
it is still emitted as GeoJSON.

## Spatial aggregates

With [`server.aggregates`](aggregates.md) on, spatial columns take four
aggregate functions in `select=` that return GeoJSON per group —
`col.extent()` (bounding box, `ST_Extent`), `col.union()` (`ST_Union`),
`col.collect()` (`ST_Collect`) and `col.centroid()` (centroid of the
collected geometries); geography input is cast to `geometry`:

```
GET /api/places?select=owner_id,location.extent(),location.centroid(),n:location.count()
```

`sum`/`avg`/`max`/`min` on a spatial column are `400 invalid_select`. See
[aggregates.md](aggregates.md#spatial-aggregates-pdbr-extension).

## Distance ordering

Every spatial column accepts `order=<col>.dist.asc|desc` together with a
`near=` parameter supplying the reference geometry (GeoJSON or WKT/EWKT, same
parsing as filters). Ordering compiles to the KNN operator `<->`, which a
GiST index answers directly — this is the fast "nearest N" path:

```
GET /api/places?select=name&order=location.dist.asc&near=SRID%3D4326%3BPOINT(-0.1281%2051.508)&limit=10
```

`near=` is required: `order=location.dist.asc` without it (or with an
unparseable value) is a `400 invalid_filter`
([errors.md](errors.md#code-vocabulary)) rather than a result that looks
sorted but is not (pdbq silently drops the term and falls back to primary-key
order; pdbr deliberately fails). One `near=` serves every `.dist` term in the
request, including per-embed `places.order=location.dist.desc` terms, and is
parsed for each term's column (its SRID, and `(…)::geography` for a
geography column, so the KNN distance is metres there); the PK tiebreaker is appended after the
distance term as for any order, and keyset cursors (`after=`/`before=`) walk
the distance order. A `near=` with no `.dist` term is `400 invalid_filter`. `near`
is reserved only where no column, computed column or RPC argument is named
so; there `near=` is that filter or argument and `.dist` ordering is
`400 column_not_orderable`. Plain
`order=location.asc` on a spatial column is `400 column_not_orderable`;
`.dist` on a non-spatial column is `400 invalid_filter`.

### Selecting the distance

Each spatial column also has a `<col>_distance` score pseudo-column: the
distance of the row's geometry from `near=`, selected explicitly like the
other pseudo-columns. "Places near me, with the distance" is one request:

```
GET /api/places?select=name,location_distance,metres:location_distance(spheroid=true),area_distance&order=location.dist&near=SRID%3D4326%3BPOINT(-0.1281%2051.508)&limit=10
```

It compiles to `ST_Distance(column, <near>)` in the same statement, with
`near=` bound again as its own parameter. Units follow the column: metres
(on the spheroid) on a `geography` column, SRID units on a `geometry`
column. A geometry column takes `(spheroid=true)` to measure in metres
instead (`ST_Distance(col::geography, <near>::geography)`, lon/lat SRIDs
only); the argument picks the SQL form, so it is part of the request shape,
and other score columns take no arguments (`400 invalid_select`).

`near=` is required whenever a distance column is selected, in the root, an
embed or a spread (`400 invalid_filter` without it), and serves them and
every `.dist` term alike; it also works on row paths and in mutation
responses (`select=` on `POST`/`PATCH`). Where `near` is a column or
argument filter the distance columns are not registered. They are
projections of the returned rows: they need no GiST index and ignore
`@omit order` and `filters.indexed_only`; `@omit read` removes them with
the column, and `ordering: false` removes them with `.dist`. Like every
pseudo-column they are not part of `*`, take no casts, filters or ordering,
and cost 1 per row in `server.max_cost`.

## Indexing

Spatial filters and ordering respect `filters.indexed_only` exactly like
scalar columns, so with that policy on (the default), a spatial column needs
a GiST index to be filterable or `.dist`-orderable at all — otherwise `400
column_not_filterable` / `column_not_orderable`:

```sql
CREATE INDEX places_location_idx ON places USING gist (location);
```

`filters.allow_columns` and `@filterable` open a column without an index.
This is worth doing regardless: without it, every spatial predicate is a
sequential scan.

## Configuration

The surface is generated by the built-in `postgis` plugin (priority 160,
`APIHook` only; the core parser and compiler read what it registers, no
`CompileHook` needed). Disabling it (`plugins.disabled: [postgis]`) removes
spatial filter operators (`400 invalid_filter`), pseudo-columns (`400
unknown_column`), projection arguments and spatial aggregates (`400
invalid_select`) and distance ordering (`400 invalid_filter`), and OpenAPI loses the `GeoJSON` schema and
the pseudo-column properties; reading and writing spatial columns as GeoJSON
— including `POST`/`PATCH`/`PUT` bodies, `is.null`, RPC arguments and
nested-mutation payloads — is core behaviour backed by `pdbcore/postgis` and
keeps working.

Each setting removes exactly its half: the plugin sets
`api.Surface.SpatialFilters` / `SpatialOrder` / `SpatialProjection` (and the
umbrella `Spatial` when all three are on) and `SpatialAggregates`, which `server.ParseOptions` hands
to `parse.Options`; `derived` decides whether `Resource.Pseudo` is populated
at all, so no parser gate is needed for it. Nothing is set when the model has
no spatial column.

```yaml
plugins:
  settings:
    postgis:
      filters: true      # st_* / bbox_* operators on spatial columns
      derived: true      # <col>_area, <col>_centroid, ...
      ordering: true     # order=<col>.dist.asc|desc, <col>_distance + near=
      projection: true   # select=col(transform=…,simplify=…)
      aggregates: true   # select=col.extent(), union(), collect(), centroid() (with server.aggregates)
```

Per-column control uses the usual [smart comments](smart-comments.md); the
plugin reads these tags from the catalog itself (`pdbcore/smarttags`), so
they work even with `smart-comments` disabled. `@omit filter` withdraws the
column from the filter vocabulary entirely (spatial operators and `is.null`
alike → `400 column_not_filterable`), `@omit order` from `.dist` ordering
(`400 column_not_orderable`); a bare `@omit` and `@behavior -filter/-order`
count too:

```sql
COMMENT ON COLUMN places.secret_location IS '@omit filter,order';
```

## OpenAPI

- `components.schemas.GeoJSON`: an object schema with `type` (enum of the
  seven RFC 7946 geometry types, `GeometryCollection` included),
  `coordinates` and `geometries`; emitted once (`Builder.Extra.Schemas`),
  only when a spatial column exists.
- Every spatial column property is `$ref: '#/components/schemas/GeoJSON'`
  (nullable per the column) via the plugin's type override
  (`Builder.Extra.Types["geometry"|"geography"]`, consulted by
  `Built.TypeSchema`). Request-body schemas (`.insert`/`.update`/`.upsert`
  and RPC `POST` bodies) use `oneOf: [GeoJSON, string]` to admit WKT/EWKT
  (`Builder.Extra.InputTypes`, `postgis.InputSchema()`).
- `components.schemas.GeoJSONFeature` and `GeoJSONFeatureCollection`
  (`postgis.FeatureSchema` / `FeatureCollectionSchema`) describe the
  `application/geo+json` bodies. Resources with a plain spatial column list
  `application/geo+json` in the `200` (and `206`) content of the list route
  (`FeatureCollection`), the key routes and single-row RPCs (`Feature`) and
  `SETOF <table>` RPCs, extend the `Accept` header parameter enum and
  document a `geometry` query parameter.
- Pseudo-columns are `readOnly` properties on the resource schema (not on
  `.insert`/`.update`/`.upsert`), typed from `PseudoColumn.Schema` (`number`,
  `integer`, `string`, `boolean`, or `GeoJSON`), each described with its
  PostGIS function and `Select explicitly: select=<key>`.
- Query parameters are rendered by the core generator from what the plugin
  leaves on the model: the column's filter parameter lists the spatial vocabulary from
  `Built.Extra.FilterOps["geometry"|"geography"]` (the 29 operators above in
  `pdbcore/postgis.Ops` order, then `is`; just `is` under `filters: false`) in `x-pdbr-filter-ops`
  instead of the scalar ops; resources with a `.dist`-orderable spatial
  column (`Surface.SpatialOrder` and the column in `OrderColumns`) get a
  `near` query parameter and their `order` description mentions
  `<col>.dist`; the `select` description mentions
  `(transform=,simplify=,buffer=,precision=)` under
  `Surface.SpatialProjection`.

## Notes and limits

- **Injection safety.** Geometries, bounding-box corners, distances, buffer
  distances and GeoJSON precisions are all bind parameters; the compiled SQL text is a pure function of the request
  shape. This is enforced by `FuzzSpatial` in `internal/compile` (part of
  `make fuzz`), the spatial twin of the `FuzzFilter` invariant.
- **Goldens.** `spatial_filter`, `spatial_dwithin`, `spatial_bbox_directional`,
  `spatial_equality`, `spatial_output`, `spatial_accessors`,
  `spatial_accessor_typmod`, `spatial_order`, `spatial_mutation`,
  `spatial_precision_buffer`, `spatial_precision_geojson` in `internal/compile/testdata` (the
  golden harness applies the plugin so pseudo-columns resolve); e2e in
  `test/e2e_postgis_test.go`.
- **Malformed input fails fast.** A value that is neither valid GeoJSON nor
  plausible WKT is rejected at compile time with a `400` problem, rather than
  reaching Postgres as a type error mid-statement.
- **CSV.** With `Accept: text/csv` spatial values and GeoJSON pseudo-columns
  are emitted as their JSON text in the cell.
- **Not covered:** raster and topology types. `geometry(TypeName, SRID)`
  type modifiers only decide which accessors a column gets; they are not
  reflected in the OpenAPI value schema (every spatial column is the same
  `GeoJSON` schema regardless of its declared geometry subtype; a
  mismatched subtype or SRID fails in PostgreSQL as `22023` → `400
  pg:22023`).
- **Geometry arrays** (`geometry[]`) are read as arrays of GeoJSON and
  written element-wise, but take no spatial operators, accessors or `.dist`.

## Related

- [smart-comments.md](smart-comments.md) — `@omit filter,order`, `@filterable`.
- [plugins.md](plugins.md) — `ParamSet.SpatialValue` for plugins that build
  their own DML over spatial columns.
