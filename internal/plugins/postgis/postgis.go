// Package postgis is the built-in plugin (priority 160) that turns PostGIS
// geometry/geography columns into a query surface: spatial filter operators
// (st_*, bbox_*), derived pseudo-columns (<col>_area, <col>_centroid, ...),
// select= projection arguments (transform=, simplify=, buffer=,
// precision=), distance ordering
// (order=<col>.dist with near=) and the <col>_distance score from near=,
// and the spatial aggregate functions
// (col.extent(), col.union(), col.collect(), col.centroid(); with
// server.aggregates), and on postgis_raster columns the metadata, pixel
// value and summary statistics pseudo-columns (<col>_width, ...,
// <col>_value(band=) at near=, <col>_summary_stats(band=)) and the
// intersects filter. Reading and writing GeoJSON, and keeping raster
// values out of every response, is core; the
// plugin only switches the grammar on (api.Surface), registers the
// pseudo-columns on each resource, applies @omit filter|order per column
// straight from the catalog comments, and documents the surface in OpenAPI
// (GeoJSON component schema, spatial columns referencing it, read-only
// pseudo-column properties). It is APIHook only: parse and compile read
// what it registered.
package postgis

import (
	"context"
	"sort"
	"strings"
	"unicode"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"
	"github.com/suprbdev/pdbcore/smarttags"

	"github.com/suprbdev/pdbr/internal/api"
)

// SchemaName is the OpenAPI component schema for GeoJSON geometries.
const SchemaName = postgis.TypeName

// Plugin implements plugin.APIHook.
type Plugin struct {
	filters    bool
	derived    bool
	ordering   bool
	projection bool
	aggregates bool
}

// New builds the plugin from its plugins.settings.postgis map: filters,
// derived, ordering, projection, aggregates (all default true); unknown
// keys are ignored.
func New(settings map[string]any) *Plugin {
	p := &Plugin{filters: true, derived: true, ordering: true, projection: true, aggregates: true}
	for key, target := range map[string]*bool{
		"filters":    &p.filters,
		"derived":    &p.derived,
		"ordering":   &p.ordering,
		"projection": &p.projection,
		"aggregates": &p.aggregates,
	} {
		if v, ok := settings[key].(bool); ok {
			*target = v
		}
	}
	return p
}

// Name is the plugin name (plugins.disabled / plugins.settings key).
func (p *Plugin) Name() string { return "postgis" }

// Priority 160 runs after smart-comments (50) and advanced-filters (150), so
// a column those removed is never given a spatial surface.
func (p *Plugin) Priority() int { return 160 }

// Settings reports the effective settings (filters, derived, ordering,
// projection).
func (p *Plugin) Settings() (filters, derived, ordering, projection bool) {
	return p.filters, p.derived, p.ordering, p.projection
}

// TransformAPI adds the spatial surface. Without a spatial or raster
// column in the model it is a no-op, so routes and OpenAPI stay
// byte-identical to a database without PostGIS.
func (p *Plugin) TransformAPI(_ context.Context, b *api.Builder) error {
	found := false
	for _, key := range sortedKeys(b.Resources) {
		r := b.Resources[key]
		for _, f := range r.Columns {
			if !postgis.IsSpatial(f.Column.PGType) {
				continue
			}
			found = true
			p.applyOmits(b, r, f)
			if p.derived {
				p.addPseudoColumns(b, r, f)
			}
			if p.ordering && !r.NameTaken("near") {
				addDistanceColumn(b, r, f)
			}
		}
		for _, f := range r.Rasters {
			if f.Column.IsArray {
				continue
			}
			found = true
			p.applyOmits(b, r, f)
			if p.derived {
				addRasterColumns(b, r, f)
			}
		}
	}
	if !found {
		return nil
	}
	b.Surface.Spatial = p.filters && p.ordering && p.projection
	b.Surface.SpatialFilters = p.filters
	b.Surface.SpatialOrder = p.ordering
	b.Surface.SpatialProjection = p.projection
	b.Surface.SpatialAggregates = p.aggregates
	if b.Extra.Schemas == nil {
		b.Extra.Schemas = map[string]*api.Schema{}
	}
	if b.Extra.Types == nil {
		b.Extra.Types = map[string]*api.Schema{}
	}
	if b.Extra.FilterOps == nil {
		b.Extra.FilterOps = map[string][]string{}
	}
	if _, ok := b.Extra.Schemas[SchemaName]; !ok {
		b.Extra.Schemas[SchemaName] = GeoJSONSchema()
	}
	if _, ok := b.Extra.Schemas[api.GeoJSONFeature]; !ok {
		b.Extra.Schemas[api.GeoJSONFeature] = FeatureSchema()
	}
	if _, ok := b.Extra.Schemas[api.GeoJSONFeatureCollection]; !ok {
		b.Extra.Schemas[api.GeoJSONFeatureCollection] = FeatureCollectionSchema()
	}
	ops := []string{"is"}
	if p.filters {
		ops = FilterOps()
	}
	if b.Extra.InputTypes == nil {
		b.Extra.InputTypes = map[string]*api.Schema{}
	}
	for _, t := range []string{"geometry", "geography"} {
		if _, ok := b.Extra.Types[t]; !ok {
			b.Extra.Types[t] = api.SchemaRef(SchemaName)
		}
		if _, ok := b.Extra.InputTypes[t]; !ok {
			b.Extra.InputTypes[t] = InputSchema()
		}
		b.Extra.FilterOps[t] = ops
	}
	b.Extra.FilterOps[postgis.RasterTypeName] = RasterFilterOps(p.filters)
	return nil
}

// RasterFilterOps lists the filter operators of a raster column for OpenAPI
// x-pdbr-filter-ops: intersects (with the filters setting) and is.
func RasterFilterOps(filters bool) []string {
	if filters {
		return []string{"intersects", "is"}
	}
	return []string{"is"}
}

// addRasterColumns registers the pseudo-columns of a raster column:
// <key>_<accessor> for each header-only metadata accessor
// (postgis.RasterAccessors: width, height, num_bands, srid, pixel_width,
// pixel_height, envelope), <key>_value(band=) (the pixel value at near=,
// unless near is a column or computed column of the resource) and
// <key>_summary_stats(band=). A real, raster, computed or other
// pseudo-column owning a key keeps it.
func addRasterColumns(b *api.Builder, r *api.Resource, f *api.Field) {
	add := func(suffix string, pc *api.PseudoColumn) {
		key := f.Key + "_" + suffix
		if r.Field(key) != nil || r.Raster(key) != nil || r.Computed[key] != nil || r.Pseudo[key] != nil {
			b.Warn("postgis: raster pseudo-column skipped: the key is taken", "resource", r.Path, "column", f.Column.Name, "key", key)
			return
		}
		if r.Pseudo == nil {
			r.Pseudo = map[string]*api.PseudoColumn{}
		}
		pc.Key, pc.Column = key, f.Column
		r.Pseudo[key] = pc
	}
	for i := range postgis.RasterAccessors {
		a := postgis.RasterAccessors[i]
		add(RasterAccessor(a), &api.PseudoColumn{
			Raster:      &api.RasterValue{Accessor: &a},
			Schema:      rasterSchema(a.Type, a.GeoJSON),
			Description: a.Description,
		})
	}
	if !r.NameTaken("near") {
		add("value", &api.PseudoColumn{
			Raster:      &api.RasterValue{Value: true},
			Score:       &api.Score{Param: "near"},
			Schema:      &api.Schema{Type: "number", Format: "double"},
			Description: "Pixel value (ST_Value) of band band= (default 1) at the near= point, reprojected to the raster's SRID; null for a NODATA pixel, a point outside the raster or a missing band. near= is required to select it.",
		})
	}
	add("summary_stats", &api.PseudoColumn{
		Raster:      &api.RasterValue{SummaryStats: true},
		Schema:      SummaryStatsSchema(),
		Description: "Summary statistics (ST_SummaryStats, NODATA pixels excluded) of band band= (default 1); null for a NULL raster or a missing band. Reads every pixel of the band.",
	})
}

// SummaryStatsSchema is the OpenAPI schema of a raster's summary
// statistics object.
func SummaryStatsSchema() *api.Schema {
	s := &api.Schema{Type: "object", Properties: map[string]*api.Schema{}}
	for _, f := range postgis.RasterSummaryStatsFields {
		typ := "number"
		if f.Type == "Int" {
			typ = "integer"
		}
		s.Properties[f.Name] = &api.Schema{Type: []string{typ, "null"}, Description: f.Description}
	}
	return s
}

func rasterSchema(typ string, geoJSON bool) *api.Schema {
	switch {
	case geoJSON:
		return api.SchemaRef(SchemaName)
	case typ == "Int":
		return &api.Schema{Type: "integer", Format: "int32"}
	}
	return &api.Schema{Type: "number", Format: "double"}
}

// InputSchema is the request-body shape of a spatial value: a GeoJSON
// geometry object or a WKT/EWKT string.
func InputSchema() *api.Schema {
	return &api.Schema{OneOf: []*api.Schema{
		api.SchemaRef(SchemaName),
		{Type: "string", Description: "WKT or EWKT (SRID=4326;POINT(...)) text."},
	}}
}

// applyOmits honours @omit filter / @omit order (and a bare @omit) on the
// column comment without depending on the smart-comments plugin.
func (p *Plugin) applyOmits(b *api.Builder, r *api.Resource, f *api.Field) {
	skipFilter, skipOrder := Omits(f.Column.Comment)
	if skipFilter {
		b.RemoveFilterColumn(r, f.Key)
	}
	if skipOrder {
		b.RemoveOrderColumn(r, f.Key)
	}
}

// Omits reports which halves of the spatial surface a column comment removes
// (@omit filter, @omit order, @behavior -filter/-order, or a bare @omit).
func Omits(comment string) (skipFilter, skipOrder bool) {
	tags, _ := smarttags.Parse(comment)
	om, everything := tags.Omits()
	return everything || om["filter"], everything || om["order"]
}

// addPseudoColumns registers <key>_<accessor> for every derived accessor
// that applies to the column's typmod (see Applies); a real or computed
// column that already owns the name keeps it.
func (p *Plugin) addPseudoColumns(b *api.Builder, r *api.Resource, f *api.Field) {
	for _, d := range postgis.Deriveds {
		if !Applies(d, f.Column) {
			continue
		}
		acc := Accessor(d)
		key := f.Key + "_" + acc
		switch {
		case r.Field(key) != nil:
			b.Warn("postgis: pseudo-column skipped: a real column owns the key", "resource", r.Path, "column", f.Column.Name, "key", key)
			continue
		case r.Computed[key] != nil:
			b.Warn("postgis: pseudo-column skipped: a computed column owns the key", "resource", r.Path, "column", f.Column.Name, "key", key)
			continue
		case r.Pseudo[key] != nil:
			continue
		}
		if r.Pseudo == nil {
			r.Pseudo = map[string]*api.PseudoColumn{}
		}
		r.Pseudo[key] = &api.PseudoColumn{
			Key:         key,
			Column:      f.Column,
			Accessor:    acc,
			Schema:      derivedSchema(d),
			Description: d.Description,
		}
	}
}

// addDistanceColumn registers the <key>_distance score pseudo-column: the
// distance of the column from near= (ST_Distance; metres on geography, SRID
// units on geometry, metres with (spheroid=true) on geometry). It is a
// projection of the returned rows, so @omit order and the filter policy
// leave it alone; a real, computed or derived column owning the key keeps
// it.
func addDistanceColumn(b *api.Builder, r *api.Resource, f *api.Field) {
	key := f.Key + "_distance"
	if r.Field(key) != nil || r.Computed[key] != nil || r.Pseudo[key] != nil {
		b.Warn("postgis: distance pseudo-column skipped: the key is taken", "resource", r.Path, "column", f.Column.Name, "key", key)
		return
	}
	desc := "Distance from near=, required to select it: "
	if postgis.FlavourOf(f.Column.PGType) == postgis.Geography {
		desc += "metres."
	} else {
		desc += "in the units of the column's SRID, or metres on the spheroid with (spheroid=true) (lon/lat SRIDs only)."
	}
	if r.Pseudo == nil {
		r.Pseudo = map[string]*api.PseudoColumn{}
	}
	r.Pseudo[key] = &api.PseudoColumn{
		Key: key, Column: f.Column,
		Score:       &api.Score{Param: "near"},
		Schema:      &api.Schema{Type: "number"},
		Description: desc,
	}
}

// Applies reports whether the derived accessor can be non-null on col
// given its typmod (introspect.Column.Spatial): x/y only on Point columns,
// z/m only on Point columns with that dimension, start_point/end_point only
// on single-curve columns. A column without a known typmod (unconstrained,
// or a catalog that predates it) gets every accessor.
func Applies(d postgis.Derived, col *introspect.Column) bool {
	if col.Spatial == nil {
		return true
	}
	return d.AppliesTo(string(col.Spatial.Subtype), col.Spatial.HasZ, col.Spatial.HasM)
}

// Accessor is the pseudo-column suffix of a derived accessor: the pdbcore
// suffix in snake_case (GeometryType -> geometry_type, IsValid -> is_valid).
func Accessor(d postgis.Derived) string { return snake(d.Suffix) }

// RasterAccessor is the pseudo-column suffix of a raster metadata accessor:
// the pdbcore suffix in snake_case (NumBands -> num_bands).
func RasterAccessor(a postgis.RasterAccessor) string { return snake(a.Suffix) }

func snake(suffix string) string {
	var sb strings.Builder
	for i, ch := range suffix {
		if unicode.IsUpper(ch) {
			if i > 0 {
				sb.WriteByte('_')
			}
			sb.WriteRune(unicode.ToLower(ch))
			continue
		}
		sb.WriteRune(ch)
	}
	return sb.String()
}

// Accessors lists the pseudo-column suffixes in declaration order.
func Accessors() []string {
	out := make([]string, len(postgis.Deriveds))
	for i, d := range postgis.Deriveds {
		out[i] = Accessor(d)
	}
	return out
}

func derivedSchema(d postgis.Derived) *api.Schema {
	switch {
	case d.GeoJSON:
		return api.SchemaRef(SchemaName)
	case d.Type == "Float":
		return &api.Schema{Type: "number", Format: "double"}
	case d.Type == "Int":
		return &api.Schema{Type: "integer", Format: "int32"}
	case d.Type == "Boolean":
		return &api.Schema{Type: "boolean"}
	default:
		return &api.Schema{Type: "string"}
	}
}

// GeoJSONSchema is the components.schemas.GeoJSON entry: an RFC 7946
// geometry object (type + coordinates, or a GeometryCollection).
func GeoJSONSchema() *api.Schema {
	return &api.Schema{
		Type:        "object",
		Description: "GeoJSON geometry (RFC 7946). Output is always GeoJSON; input also accepts a WKT/EWKT string. Feature and FeatureCollection documents are unwrapped.",
		Properties: map[string]*api.Schema{
			"type": {Type: "string", Enum: []string{
				"Point", "LineString", "Polygon", "MultiPoint", "MultiLineString", "MultiPolygon", "GeometryCollection",
			}},
			"coordinates": {Type: "array", Items: &api.Schema{}},
			"geometries":  {Type: "array", Items: api.SchemaRef(SchemaName)},
		},
		Required: []string{"type"},
	}
}

// FeatureSchema is the components.schemas.GeoJSONFeature entry: the row
// representation of Accept: application/geo+json.
func FeatureSchema() *api.Schema {
	return &api.Schema{
		Type:        "object",
		Description: "GeoJSON Feature (RFC 7946) of one row: geometry is the chosen spatial column, properties the other selected columns and embeds.",
		Properties: map[string]*api.Schema{
			"type":       {Type: "string", Enum: []string{"Feature"}},
			"geometry":   api.Nullable(api.SchemaRef(SchemaName)),
			"properties": {Type: "object"},
		},
		Required: []string{"type", "geometry", "properties"},
	}
}

// FeatureCollectionSchema is the components.schemas.GeoJSONFeatureCollection
// entry: the list representation of Accept: application/geo+json.
func FeatureCollectionSchema() *api.Schema {
	return &api.Schema{
		Type:        "object",
		Description: "GeoJSON FeatureCollection (RFC 7946): one Feature per row of the page.",
		Properties: map[string]*api.Schema{
			"type":     {Type: "string", Enum: []string{"FeatureCollection"}},
			"features": {Type: "array", Items: api.SchemaRef(api.GeoJSONFeature)},
		},
		Required: []string{"type", "features"},
	}
}

// FilterOps lists the spatial filter operator names in pdbr's grammar
// (st_intersects, ..., st_dwithin, st_beyond, bbox_intersects, ...) plus
// `is`, for OpenAPI x-pdbr-filter-ops.
func FilterOps() []string {
	var out []string
	for _, op := range postgis.Ops {
		if op.Kind() == postgis.ArgBoolean {
			continue
		}
		out = append(out, OpName(op))
	}
	return append(out, "is")
}

// OpName maps a pdbcore operator (pdbq spelling, e.g. bboxIntersects,
// dwithin) to pdbr's query-string spelling (bbox_intersects, st_dwithin).
func OpName(op postgis.Op) string {
	name := op.Name
	if strings.HasPrefix(name, "bbox") {
		return "bbox_" + strings.ToLower(strings.TrimPrefix(name, "bbox"))
	}
	return "st_" + strings.ToLower(name)
}

// SpatialColumns returns the resource's geometry/geography fields in
// catalog order.
func SpatialColumns(r *api.Resource) []*api.Field {
	var out []*api.Field
	for _, f := range r.Columns {
		if postgis.IsSpatial(f.Column.PGType) {
			out = append(out, f)
		}
	}
	return out
}

// HasSpatialColumn reports whether any exposed column of the catalog table
// is spatial.
func HasSpatialColumn(t *introspect.Table) bool {
	for _, c := range t.Columns {
		if postgis.IsSpatial(c.PGType) {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
