package api

import (
	"bytes"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/inflect"
	"github.com/suprbdev/pdbr/internal/testcatalog"
)

func geoCatalog() *introspect.Catalog {
	return testcatalog.Geo(testcatalog.Contrib(testutil.FixtureCatalog()))
}

func geoOptions(indexedOnly bool) Options {
	opts := contribOptions(indexedOnly)
	opts.AllowColumns[testcatalog.ContribSchema+".tiles"] = []string{"rast"}
	return opts
}

// TestCubeEarthSurface: cube columns take cs/cd/ov and the vec_*_lt
// thresholds where a GiST index (or the policy) admits them, order by the
// three cube distances to vec= and score them everywhere; earth columns
// take earth_lt and order by earth_dist to latlon=; point columns get the
// <@> surface only where the policy admits unindexed surface.
func TestCubeEarthSurface(t *testing.T) {
	b := build(t, geoCatalog(), geoOptions(true))
	shapes, sites := b.ByPath["kb.shapes"], b.ByPath["kb.sites"]
	if got, want := b.FilterOps(shapes, shapes.Table.Column("box")), []string{"is", "cs", "cd", "ov", "vec_l2_lt", "vec_l1_lt", "vec_chebyshev_lt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("box ops = %v, want %v", got, want)
	}
	if got, want := b.FilterOps(sites, sites.Table.Column("loc")), []string{"is", "earth_lt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("loc ops = %v, want %v", got, want)
	}
	var orders []string
	for _, o := range b.ContribOrders(shapes, shapes.Table.Column("box")) {
		orders = append(orders, o.Name+"/"+o.Param)
	}
	if want := []string{"vec_l2/vec", "vec_l1/vec", "vec_chebyshev/vec"}; !reflect.DeepEqual(orders, want) {
		t.Errorf("box orders = %v, want %v", orders, want)
	}
	if got := b.ContribOrders(shapes, shapes.Table.Column("pt")); len(got) != 0 {
		t.Errorf("unindexed cube column orders under indexed_only: %v", got)
	}
	if got := b.ContribOrders(sites, sites.Table.Column("ll")); len(got) != 0 {
		t.Errorf("unlisted point column orders under indexed_only: %v", got)
	}
	want := []string{
		"box_chebyshev_distance=vec:chebyshevDistance", "box_l1_distance=vec:l1Distance", "box_l2_distance=vec:l2Distance",
		"pt_chebyshev_distance=vec:chebyshevDistance", "pt_l1_distance=vec:l1Distance", "pt_l2_distance=vec:l2Distance",
	}
	if got := scoreKeys(shapes); !reflect.DeepEqual(got, want) {
		t.Errorf("kb.shapes scores = %v\nwant %v", got, want)
	}
	if got := scoreKeys(sites); !reflect.DeepEqual(got, []string{"loc_earth_distance=latlon:earthDistance"}) {
		t.Errorf("kb.sites scores = %v", got)
	}

	opts := geoOptions(true)
	opts.AllowColumns[testcatalog.ContribSchema+".sites"] = []string{"ll"}
	b = build(t, geoCatalog(), opts)
	sites = b.ByPath["kb.sites"]
	if got := b.FilterOps(sites, sites.Table.Column("ll")); !slices.Contains(got, "earth_miles_lt") {
		t.Errorf("allowlisted point ops = %v", got)
	}
	if got := scoreKeys(sites); !reflect.DeepEqual(got, []string{"ll_earth_distance_miles=latlon:earthDistanceMiles", "loc_earth_distance=latlon:earthDistance"}) {
		t.Errorf("kb.sites scores with ll allowlisted = %v", got)
	}

	get := b.OpenAPI.Paths["/kb/sites"].Get
	ll := paramByName(get.Parameters, "latlon")
	if ll == nil || !strings.Contains(ll.Description, "<lat>,<lon>") {
		t.Errorf("latlon param = %+v", ll)
	}
	if order := paramByName(get.Parameters, "order").Description; !strings.Contains(order, "latlon=<lat>,<lon>") || !strings.Contains(order, "loc.earth_dist, ll.earth_miles") || strings.Contains(order, "Orderable columns: id, name, loc") {
		t.Errorf("order description: %s", order)
	}
	if p := paramByName(get.Parameters, "loc"); p == nil || !reflect.DeepEqual(p.XFilterOps, []string{"is", "earth_lt"}) {
		t.Errorf("loc filter param = %+v", p)
	}
	if paramByName(b.OpenAPI.Paths["/kb/shapes"].Get.Parameters, "latlon") != nil {
		t.Error("kb.shapes documents latlon")
	}
	if paramByName(b.OpenAPI.Paths["/kb/shapes"].Get.Parameters, "vec") == nil {
		t.Error("kb.shapes lacks vec")
	}

	schemas := b.OpenAPI.Components.Schemas
	loc := schemas["kb.sites"].Properties["loc"]
	if !reflect.DeepEqual(loc.Type, []string{"object", "null"}) || loc.Properties["lat"] == nil || !reflect.DeepEqual(loc.Required, []string{"lat", "lon"}) {
		t.Errorf("earth output schema = %+v", loc)
	}
	if in := schemas["kb.sites.insert"].Properties["loc"]; in.Properties["lon"] == nil {
		t.Errorf("earth input schema = %+v", in)
	}
	box := schemas["kb.shapes"].Properties["box"]
	if len(box.AnyOf) != 2 || len(box.AnyOf[0].OneOf) != 2 || box.AnyOf[0].OneOf[1].MaxItems == nil {
		t.Errorf("cube output schema = %+v", box)
	}
	if in := schemas["kb.shapes.insert"].Properties["box"]; len(in.AnyOf[0].OneOf) != 3 || in.AnyOf[0].OneOf[2].Type != "string" {
		t.Errorf("cube input schema = %+v", in)
	}
	if p := schemas["kb.shapes"].Properties["box_chebyshev_distance"]; p == nil || !p.ReadOnly || !strings.Contains(p.Description, "vec=") {
		t.Errorf("cube score property = %+v", p)
	}
}

// TestCubeEarthGatedOnExtensions: without cube and earthdistance recorded,
// cube and earth columns get no vocabulary, and installing earthdistance
// leaves the point columns of a database where the policy does not admit
// unindexed surface unchanged.
func TestCubeEarthGatedOnExtensions(t *testing.T) {
	cat := geoCatalog()
	var kept []*introspect.Extension
	for _, e := range cat.Extensions {
		if e.Name != "cube" && e.Name != "earthdistance" {
			kept = append(kept, e)
		}
	}
	cat.Extensions = kept
	b := build(t, cat, geoOptions(false))
	shapes := b.ByPath["kb.shapes"]
	if got := b.FilterOps(shapes, shapes.Table.Column("box")); slices.Contains(got, "vec_l2_lt") || slices.Contains(got, "cs") {
		t.Errorf("cube ops without the extension = %v", got)
	}
	for _, k := range append(scoreKeys(shapes), scoreKeys(b.ByPath["kb.sites"])...) {
		if !strings.HasPrefix(k, "name_") {
			t.Errorf("score without the extensions: %s", k)
		}
	}

	plain := build(t, testutil.FixtureCatalog(), defaultOptions())
	withED := testutil.FixtureCatalog()
	withED.Extensions = append(withED.Extensions,
		&introspect.Extension{Name: "cube", Version: "1.5", Schema: "public"},
		&introspect.Extension{Name: "earthdistance", Version: "1.2", Schema: "public"})
	if !bytes.Equal(openAPIJSON(t, build(t, withED, defaultOptions())), openAPIJSON(t, plain)) {
		t.Error("installing cube and earthdistance without such columns changed the OpenAPI document")
	}
}

// TestRasterColumns: a raster column is not one of the readable columns
// (select=*, the read schema, the ETag hash), stays writable, filterable
// under the policy only when allowlisted, never orderable, and RemoveField
// (@omit read) drops it with its pseudo-columns.
func TestRasterColumns(t *testing.T) {
	b := build(t, geoCatalog(), geoOptions(true))
	tiles := b.ByPath["kb.tiles"]
	if tiles.Field("rast") != nil || tiles.Raster("rast") == nil || tiles.Raster("history") == nil {
		t.Fatalf("raster fields: columns %v, rasters %v", fieldKeys(tiles.Columns), fieldKeys(tiles.Rasters))
	}
	if !tiles.FilterColumns["rast"] || tiles.FilterColumns["history"] || tiles.OrderColumns["rast"] {
		t.Errorf("raster policy: filter %v order %v", tiles.FilterColumns, tiles.OrderColumns)
	}
	if tiles.InputColumns["rast"] == nil {
		t.Error("raster column not writable")
	}
	if !tiles.NameTaken("rast") {
		t.Error("raster key not taken")
	}
	schemas := b.OpenAPI.Components.Schemas
	if schemas["kb.tiles"].Properties["rast"] != nil || schemas["kb.tiles"].Properties["history"] != nil {
		t.Error("raster column in the read schema")
	}
	in := schemas["kb.tiles.insert"]
	if p := in.Properties["rast"]; p == nil || !strings.Contains(p.Description, "Write-only") {
		t.Errorf("raster input property = %+v", in.Properties["rast"])
	}
	var keys []string
	for k := range in.Properties {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if !reflect.DeepEqual(keys, []string{"history", "id", "name", "rast"}) {
		t.Errorf("insert keys = %v", keys)
	}
	if p := paramByName(b.OpenAPI.Paths["/kb/tiles"].Get.Parameters, "rast"); p == nil || !reflect.DeepEqual(p.XFilterOps, []string{"is"}) {
		t.Errorf("raster filter param without the postgis plugin = %+v", p)
	}
	if sel := paramByName(b.OpenAPI.Paths["/kb/tiles"].Get.Parameters, "select").Description; !strings.Contains(sel, "Raster columns (rast, history) are not readable") {
		t.Errorf("select description: %s", sel)
	}

	b2 := build(t, geoCatalog(), contribOptions(true))
	if b2.ByPath["kb.tiles"].FilterColumns["rast"] {
		t.Error("unlisted raster column filterable under indexed_only")
	}

	bl := New(geoCatalog(), inflect.Default, geoOptions(true))
	r := bl.Resources["kb.tiles"]
	r.Pseudo["rast_width"] = &PseudoColumn{Key: "rast_width", Column: r.Raster("rast").Column}
	bl.RemoveField(r, "rast", false)
	if r.Raster("rast") != nil || r.Pseudo["rast_width"] != nil || r.FilterColumns["rast"] || r.InputColumns["rast"] == nil {
		t.Errorf("RemoveField(rast): rasters %v pseudo %v filter %v", fieldKeys(r.Rasters), r.Pseudo, r.FilterColumns)
	}
	bl.RemoveField(r, "history", true)
	if r.InputColumns["history"] != nil {
		t.Error("RemoveField everywhere kept the raster writable")
	}
}
