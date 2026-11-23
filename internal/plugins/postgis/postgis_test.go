package postgis

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	corepostgis "github.com/suprbdev/pdbcore/postgis"
	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/inflect"
	"github.com/suprbdev/pdbr/internal/parse"
	"github.com/suprbdev/pdbr/internal/plan"
	"github.com/suprbdev/pdbr/internal/plugin"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func placesTable(cat *introspect.Catalog) *introspect.Table {
	for _, t := range cat.Tables {
		if t.Name == "places" {
			return t
		}
	}
	return nil
}

func build(t *testing.T, cat *introspect.Catalog, p *Plugin) (*api.Builder, *api.Built) {
	t.Helper()
	b := api.New(cat, inflect.Default, api.Options{
		FilterIndexedOnly: true, Functions: true, BasePath: "/api", Schemas: []string{"public"},
		MaxPageSize: 100, Etags: true, Logger: quiet(),
	})
	if p != nil {
		if err := p.TransformAPI(context.Background(), b); err != nil {
			t.Fatal(err)
		}
	}
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	return b, built
}

func TestPluginIdentity(t *testing.T) {
	p := New(nil)
	if p.Name() != "postgis" || p.Priority() != 160 {
		t.Fatalf("name/priority = %s/%d", p.Name(), p.Priority())
	}
	if got := plugin.Hooks(p); !reflect.DeepEqual(got, []string{"api"}) {
		t.Fatalf("hooks = %v", got)
	}
}

func TestSettings(t *testing.T) {
	cases := []struct {
		in   map[string]any
		want [4]bool
	}{
		{nil, [4]bool{true, true, true, true}},
		{map[string]any{"filters": false}, [4]bool{false, true, true, true}},
		{map[string]any{"derived": false, "ordering": false}, [4]bool{true, false, false, true}},
		{map[string]any{"projection": false, "unknown": 1, "filters": "no"}, [4]bool{true, true, true, false}},
	}
	for _, tc := range cases {
		p := New(tc.in)
		f, d, o, pr := p.Settings()
		if got := [4]bool{f, d, o, pr}; got != tc.want {
			t.Errorf("New(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

type spatialSurface struct{ all, filters, order, projection, aggregates bool }

func surfaceOf(s api.Surface) spatialSurface {
	return spatialSurface{s.Spatial, s.SpatialFilters, s.SpatialOrder, s.SpatialProjection, s.SpatialAggregates}
}

func TestSurfaceMatrix(t *testing.T) {
	cases := []struct {
		settings map[string]any
		want     spatialSurface
	}{
		{nil, spatialSurface{true, true, true, true, true}},
		{map[string]any{"filters": false}, spatialSurface{false, false, true, true, true}},
		{map[string]any{"ordering": false}, spatialSurface{false, true, false, true, true}},
		{map[string]any{"projection": false}, spatialSurface{false, true, true, false, true}},
		{map[string]any{"derived": false}, spatialSurface{true, true, true, true, true}},
		{map[string]any{"aggregates": false}, spatialSurface{true, true, true, true, false}},
	}
	for _, tc := range cases {
		_, built := build(t, testutil.FixtureCatalog(), New(tc.settings))
		if got := surfaceOf(built.Surface); got != tc.want {
			t.Errorf("settings %v: surface = %+v, want %+v", tc.settings, got, tc.want)
		}
	}
}

func TestPseudoColumns(t *testing.T) {
	_, built := build(t, testutil.FixtureCatalog(), New(nil))
	places := built.ByPath["places"]
	want := []string{
		"area", "length", "perimeter", "centroid", "envelope", "srid", "geometry_type", "is_valid",
		"x", "y", "z", "m", "num_points", "num_geometries", "start_point", "end_point",
		"point_on_surface", "wkt", "ewkt", "is_empty", "is_simple", "xmin", "ymin", "xmax", "ymax",
	}
	if got := Accessors(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Accessors = %v", got)
	}
	notOnPoint := map[string]bool{"z": true, "m": true, "start_point": true, "end_point": true}
	if n := 2*len(want) - len(notOnPoint); len(places.Pseudo) != n {
		t.Fatalf("pseudo-columns = %d, want %d: %v", len(places.Pseudo), n, places.Pseudo)
	}
	for _, col := range []string{"location", "area"} {
		for _, acc := range want {
			pc := places.Pseudo[col+"_"+acc]
			if col == "location" && notOnPoint[acc] {
				if pc != nil {
					t.Errorf("pseudo %s_%s registered on a 2D Point column", col, acc)
				}
				continue
			}
			if pc == nil || pc.Column.Name != col || pc.Accessor != acc || pc.Schema == nil {
				t.Errorf("pseudo %s_%s = %+v", col, acc, pc)
			}
		}
	}
	if s := places.Pseudo["location_centroid"].Schema; s.Ref != "#/components/schemas/GeoJSON" {
		t.Errorf("centroid schema = %+v", s)
	}
	if s := places.Pseudo["location_srid"].Schema; s.Type != "integer" {
		t.Errorf("srid schema = %+v", s)
	}
	if s := places.Pseudo["area_is_valid"].Schema; s.Type != "boolean" {
		t.Errorf("is_valid schema = %+v", s)
	}
	if s := places.Pseudo["location_x"].Schema; s.Type != "number" {
		t.Errorf("x schema = %+v", s)
	}
	if s := places.Pseudo["area_start_point"].Schema; s.Ref != "#/components/schemas/GeoJSON" {
		t.Errorf("start_point schema = %+v", s)
	}
	if s := places.Pseudo["area_ewkt"].Schema; s.Type != "string" {
		t.Errorf("ewkt schema = %+v", s)
	}
	// Non-spatial resources get none.
	if len(built.ByPath["users"].Pseudo) != 0 {
		t.Errorf("users pseudo = %v", built.ByPath["users"].Pseudo)
	}
	// derived: false registers nothing.
	_, off := build(t, testutil.FixtureCatalog(), New(map[string]any{"derived": false}))
	if len(off.ByPath["places"].Pseudo) != 0 {
		t.Errorf("derived=false still registered %v", off.ByPath["places"].Pseudo)
	}
}

func TestRealColumnWins(t *testing.T) {
	cat := testutil.FixtureCatalog()
	pt := placesTable(cat)
	pt.Columns = append(pt.Columns, &introspect.Column{Name: "location_srid", Position: 6, PGType: "int4", TypeSchema: "pg_catalog"})
	b, built := build(t, cat, New(nil))
	places := built.ByPath["places"]
	if places.Pseudo["location_srid"] != nil {
		t.Fatal("pseudo-column shadowed a real column")
	}
	if places.Field("location_srid") == nil || len(places.Pseudo) != 45 {
		t.Fatalf("field/pseudo count: %v %d", places.Field("location_srid"), len(places.Pseudo))
	}
	found := false
	for _, w := range b.Warnings {
		if strings.Contains(w, "pseudo-column skipped") {
			found = true
		}
	}
	if !found {
		t.Errorf("no warning: %v", b.Warnings)
	}
}

func TestPseudoColumnsByTypmod(t *testing.T) {
	cases := []struct {
		spatial *introspect.SpatialType
		has     []string
		lacks   []string
	}{
		{nil, []string{"x", "z", "m", "start_point", "end_point"}, nil},
		{&introspect.SpatialType{Subtype: introspect.SpatialGeometry}, []string{"x", "z", "m", "start_point"}, nil},
		{&introspect.SpatialType{Subtype: introspect.SpatialPoint, SRID: 4326}, []string{"x", "y", "srid"}, []string{"z", "m", "start_point", "end_point"}},
		{&introspect.SpatialType{Subtype: introspect.SpatialPoint, HasZ: true}, []string{"x", "y", "z"}, []string{"m"}},
		{&introspect.SpatialType{Subtype: introspect.SpatialPoint, HasZ: true, HasM: true}, []string{"z", "m"}, nil},
		{&introspect.SpatialType{Subtype: introspect.SpatialLineString}, []string{"start_point", "end_point", "length"}, []string{"x", "y", "z", "m"}},
		{&introspect.SpatialType{Subtype: introspect.SpatialPolygon}, []string{"area", "point_on_surface", "xmin"}, []string{"x", "start_point"}},
		{&introspect.SpatialType{Subtype: "CircularString"}, []string{"start_point"}, []string{"x"}},
		{&introspect.SpatialType{Subtype: "Nonsense"}, []string{"x", "start_point"}, nil},
	}
	for _, tc := range cases {
		cat := testutil.FixtureCatalog()
		pt := placesTable(cat)
		pt.Columns[3].Spatial = tc.spatial
		_, built := build(t, cat, New(nil))
		places := built.ByPath["places"]
		for _, acc := range tc.has {
			if places.Pseudo["location_"+acc] == nil {
				t.Errorf("%+v: location_%s missing", tc.spatial, acc)
			}
		}
		for _, acc := range tc.lacks {
			if places.Pseudo["location_"+acc] != nil {
				t.Errorf("%+v: location_%s registered", tc.spatial, acc)
			}
		}
	}
}

func TestOmitTags(t *testing.T) {
	cat := testutil.FixtureCatalog()
	pt := placesTable(cat)
	pt.Columns[3].Comment = "@omit filter,order\nWhere it is."
	pt.Columns[4].Comment = "@omit filter"
	_, built := build(t, cat, New(nil))
	places := built.ByPath["places"]
	if places.FilterColumns["location"] || places.OrderColumns["location"] {
		t.Errorf("@omit filter,order not applied: %v %v", places.FilterColumns, places.OrderColumns)
	}
	if places.FilterColumns["area"] || !places.OrderColumns["area"] {
		t.Errorf("@omit filter on area: %v %v", places.FilterColumns, places.OrderColumns)
	}
	if !places.FilterColumns["id"] || !places.OrderColumns["owner_id"] {
		t.Errorf("scalar policy disturbed: %v %v", places.FilterColumns, places.OrderColumns)
	}
	if f, o := Omits("@behavior -filter"); !f || o {
		t.Errorf("behavior omit = %v %v", f, o)
	}
	if f, o := Omits("@omit"); !f || !o {
		t.Errorf("bare omit = %v %v", f, o)
	}
}

func TestIndexedOnlyPolicy(t *testing.T) {
	cat := testutil.FixtureCatalog()
	pt := placesTable(cat)
	var idx []*introspect.Index
	for _, ix := range pt.Indexes {
		if ix.Name != "places_location_idx" {
			idx = append(idx, ix)
		}
	}
	pt.Indexes = idx
	_, built := build(t, cat, New(nil))
	places := built.ByPath["places"]
	if places.FilterColumns["location"] || places.OrderColumns["location"] {
		t.Errorf("unindexed spatial column admitted: %v", places.FilterColumns)
	}
	if !places.FilterColumns["area"] || !places.OrderColumns["area"] {
		t.Errorf("GiST-indexed column dropped: %v", places.FilterColumns)
	}
}

func TestOpenAPI(t *testing.T) {
	_, built := build(t, testutil.FixtureCatalog(), New(nil))
	doc := built.OpenAPI
	geo := doc.Components.Schemas["GeoJSON"]
	if geo == nil || geo.Type != "object" || len(geo.Properties["type"].Enum) != 7 {
		t.Fatalf("GeoJSON schema = %+v", geo)
	}
	places := doc.Components.Schemas["places"]
	loc := places.Properties["location"]
	if len(loc.AnyOf) != 2 || loc.AnyOf[0].Ref != "#/components/schemas/GeoJSON" {
		t.Errorf("location property = %+v", loc)
	}
	srid := places.Properties["location_srid"]
	if srid == nil || !srid.ReadOnly || !strings.Contains(srid.Description, "select=location_srid") {
		t.Errorf("location_srid property = %+v", srid)
	}
	cen := places.Properties["area_centroid"]
	if cen == nil || !cen.ReadOnly || len(cen.AnyOf) != 2 || cen.AnyOf[0].Ref != "#/components/schemas/GeoJSON" {
		t.Errorf("area_centroid property = %+v", cen)
	}
	if s := built.TypeSchema("geography", "public", ""); s.Ref != "#/components/schemas/GeoJSON" {
		t.Errorf("TypeSchema(geography) = %+v", s)
	}
	if ops := built.Extra.FilterOps["geometry"]; !reflect.DeepEqual(ops, FilterOps()) || len(ops) != len(corepostgis.Ops) {
		t.Errorf("FilterOps = %v", ops)
	}
	_, off := build(t, testutil.FixtureCatalog(), New(map[string]any{"filters": false}))
	if ops := off.Extra.FilterOps["geography"]; !reflect.DeepEqual(ops, []string{"is"}) {
		t.Errorf("filters=false FilterOps = %v", ops)
	}
}

func TestDisabledAndNoSpatial(t *testing.T) {
	// Plugin absent: no GeoJSON schema, no pseudo-columns, surface off.
	_, off := build(t, testutil.FixtureCatalog(), nil)
	if off.OpenAPI.Components.Schemas["GeoJSON"] != nil || len(off.ByPath["places"].Pseudo) != 0 || surfaceOf(off.Surface) != (spatialSurface{}) {
		t.Errorf("baseline has spatial surface: %+v", off.Surface)
	}
	// Plugin present but no spatial column: byte-identical OpenAPI.
	cat := testutil.FixtureCatalog()
	var tables []*introspect.Table
	for _, tb := range cat.Tables {
		if tb.Name != "places" {
			tables = append(tables, tb)
		}
	}
	cat.Tables = tables
	_, a := build(t, cat, nil)
	_, b := build(t, cat, New(nil))
	ja, _ := a.OpenAPI.JSON()
	jb, _ := b.OpenAPI.JSON()
	if string(ja) != string(jb) {
		t.Error("OpenAPI differs without a spatial column")
	}
	if surfaceOf(b.Surface) != (spatialSurface{}) {
		t.Errorf("surface set without spatial columns: %+v", b.Surface)
	}
}

func TestFilterOpsMatchParser(t *testing.T) {
	ops := FilterOps()
	if len(ops) != len(plan.SpatialOps)+1 || ops[len(ops)-1] != "is" {
		t.Fatalf("FilterOps = %v", ops)
	}
	for _, name := range ops[:len(ops)-1] {
		if _, ok := plan.SpatialOps[plan.Op(name)]; !ok {
			t.Errorf("FilterOps %q is not a parser operator", name)
		}
	}
	for op, core := range plan.SpatialOps {
		spec, ok := corepostgis.OpByName(core)
		if !ok || OpName(spec) != string(op) {
			t.Errorf("OpName(%s) = %q, want %q", core, OpName(spec), op)
		}
	}
}

// TestParseGates drives the parser with the Surface flags the plugin sets so
// each setting removes exactly its half of the grammar.
func TestParseGates(t *testing.T) {
	_, built := build(t, testutil.FixtureCatalog(), New(nil))
	requests := map[string]string{
		"filters":    "/places?location=st_intersects.SRID%3D4326%3BPOINT(1%202)",
		"ordering":   "/places?order=location.dist.asc&near=POINT(1%202)",
		"projection": "/places?select=location(transform=3857)",
		"derived":    "/places?select=location_srid",
	}
	parseWith := func(t *testing.T, opts parse.Options, target string) error {
		t.Helper()
		m, ok := built.Match(target[:strings.IndexAny(target+"?", "?")])
		if !ok {
			t.Fatalf("no route for %s", target)
		}
		_, err := parse.Parse(built, m, httptest.NewRequest("GET", "/api"+target, nil), opts)
		return err
	}
	for name, target := range requests {
		all := parse.Options{Spatial: true}
		if err := parseWith(t, all, target); err != nil {
			t.Errorf("%s with Spatial: %v", name, err)
		}
		none := parse.Options{}
		err := parseWith(t, none, target)
		if name == "derived" {
			if err != nil {
				t.Errorf("pseudo-columns must not depend on a gate: %v", err)
			}
		} else if err == nil {
			t.Errorf("%s parsed with every gate off", name)
		}
	}
	for name, opts := range map[string]parse.Options{
		"filters":    {SpatialFilters: true},
		"ordering":   {SpatialOrder: true},
		"projection": {SpatialProjection: true},
	} {
		for other, target := range requests {
			err := parseWith(t, opts, target)
			switch {
			case other == name || other == "derived":
				if err != nil {
					t.Errorf("gate %s: %s failed: %v", name, other, err)
				}
			case err == nil:
				t.Errorf("gate %s let %s through", name, other)
			}
		}
	}
}
