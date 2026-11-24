package postgis

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/inflect"
	"github.com/suprbdev/pdbr/internal/plugins/smartcomments"
	"github.com/suprbdev/pdbr/internal/testcatalog"
)

func buildTiles(t *testing.T, p *Plugin, comment string) *api.Built {
	t.Helper()
	cat := testcatalog.Geo(testcatalog.Contrib(testutil.FixtureCatalog()))
	cat.Table(testcatalog.ContribSchema, "tiles").Column("rast").Comment = comment
	sc := smartcomments.New()
	if err := sc.TransformCatalog(context.Background(), cat); err != nil {
		t.Fatal(err)
	}
	b := api.New(cat, inflect.Default, api.Options{
		FilterIndexedOnly: true, Functions: true, BasePath: "/api", Schemas: []string{"public", testcatalog.ContribSchema},
		AllowColumns: map[string][]string{testcatalog.ContribSchema + ".tiles": {"rast"}},
		MaxPageSize:  100, Etags: true, Logger: quiet(),
	})
	if err := sc.TransformAPI(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if p != nil {
		if err := p.TransformAPI(context.Background(), b); err != nil {
			t.Fatal(err)
		}
	}
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	return built
}

func rasterKeys(r *api.Resource) []string {
	var out []string
	for _, k := range sortedKeys(r.Pseudo) {
		if r.Pseudo[k].Raster != nil {
			out = append(out, k)
		}
	}
	return out
}

// TestRasterPseudoColumns: a raster column gets the metadata accessors,
// the pixel value at ~near= and the summary statistics; raster arrays get
// nothing; OpenAPI documents them, the near parameter, the intersects
// filter and the summary-statistics object.
func TestRasterPseudoColumns(t *testing.T) {
	built := buildTiles(t, New(DefaultSettings()), "")
	tiles := built.ByPath["kb.tiles"]
	want := []string{"~rast_envelope", "~rast_height", "~rast_num_bands", "~rast_pixel_height", "~rast_pixel_width", "~rast_srid", "~rast_summary_stats", "~rast_value", "~rast_width"}
	if got := rasterKeys(tiles); !reflect.DeepEqual(got, want) {
		t.Fatalf("raster pseudo-columns = %v\nwant %v", got, want)
	}
	if v := tiles.Pseudo["~rast_value"]; v.Score == nil || v.Score.Param != api.ParamNear || !v.Raster.Value {
		t.Errorf("~rast_value = %+v", v)
	}
	if !built.Surface.SpatialFilters {
		t.Error("raster-only model left the spatial filter surface off")
	}
	props := built.OpenAPI.Components.Schemas["kb.tiles"].Properties
	if p := props["~rast_envelope"]; p == nil || p.Ref != "#/components/schemas/GeoJSON" && (len(p.AnyOf) == 0 || p.AnyOf[0].Ref != "#/components/schemas/GeoJSON") {
		t.Errorf("~rast_envelope property = %+v", p)
	}
	if p := props["~rast_value"]; p == nil || !strings.Contains(p.Description, "select=~rast_value[(band=<integer>)]") {
		t.Errorf("~rast_value property = %+v", p)
	}
	if p := props["~rast_summary_stats"]; p == nil || p.Properties["mean"] == nil || !reflect.DeepEqual(p.Type, []string{"object", "null"}) {
		t.Errorf("~rast_summary_stats property = %+v", p)
	}
	if props["rast"] != nil {
		t.Error("raster column in the read schema")
	}
	get := built.OpenAPI.Paths["/kb/tiles"].Get
	if paramByName(get.Parameters, "~near") == nil {
		t.Error("near not documented for ~rast_value")
	}
	if p := paramByName(get.Parameters, "rast"); p == nil || !reflect.DeepEqual(p.XFilterOps, []string{"st_intersects", "is"}) || !strings.Contains(p.Description, "raster's SRID") {
		t.Errorf("rast filter param = %+v", p)
	}
	if sel := paramByName(get.Parameters, "select").Description; !strings.Contains(sel, "~rast_summary_stats[(band=<integer>)]") || !strings.Contains(sel, "~rast_value[(band=<integer>)]") {
		t.Errorf("select description: %s", sel)
	}

	off := buildTiles(t, New(Settings{Ordering: true, Projection: true, Aggregates: true}), "")
	if got := rasterKeys(off.ByPath["kb.tiles"]); len(got) != 0 {
		t.Errorf("derived: false kept %v", got)
	}
	if p := paramByName(off.OpenAPI.Paths["/kb/tiles"].Get.Parameters, "rast"); p == nil || !reflect.DeepEqual(p.XFilterOps, []string{"is"}) {
		t.Errorf("filters: false rast param = %+v", p)
	}
}

// TestRasterOmit: @omit read on the raster column removes its whole
// surface (pseudo-columns and filter) and keeps it writable; @omit filter
// removes the filter only.
func TestRasterOmit(t *testing.T) {
	tiles := buildTiles(t, New(DefaultSettings()), "@omit read").ByPath["kb.tiles"]
	if got := rasterKeys(tiles); len(got) != 0 || tiles.Raster("rast") != nil || tiles.FilterColumns["rast"] {
		t.Errorf("@omit read: pseudo %v, raster %v, filter %v", got, tiles.Raster("rast"), tiles.FilterColumns["rast"])
	}
	if tiles.InputColumns["rast"] == nil {
		t.Error("@omit read made the raster column read-only")
	}
	tiles = buildTiles(t, New(DefaultSettings()), "@omit filter").ByPath["kb.tiles"]
	if tiles.FilterColumns["rast"] || len(rasterKeys(tiles)) == 0 {
		t.Errorf("@omit filter: filter %v, pseudo %v", tiles.FilterColumns["rast"], rasterKeys(tiles))
	}
}

// TestRasterColumnsDoNotCollide: real columns named rast_width and near
// leave the raster pseudo-columns, the pixel value included, in place.
func TestRasterColumnsDoNotCollide(t *testing.T) {
	cat := testcatalog.Geo(testcatalog.Contrib(testutil.FixtureCatalog()))
	tiles := cat.Table(testcatalog.ContribSchema, "tiles")
	tiles.Columns = append(tiles.Columns,
		&introspect.Column{Name: "rast_width", Position: 5, PGType: "text", TypeSchema: "pg_catalog"},
		&introspect.Column{Name: "near", Position: 6, PGType: "text", TypeSchema: "pg_catalog"})
	b := api.New(cat, inflect.Default, api.Options{Schemas: []string{"public", testcatalog.ContribSchema}, MaxPageSize: 100, Logger: quiet()})
	if err := New(DefaultSettings()).TransformAPI(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	r := b.Resources[testcatalog.ContribSchema+".tiles"]
	if r.Pseudo["~rast_width"] == nil || r.Pseudo["~rast_value"] == nil {
		t.Errorf("raster pseudo-columns next to rast_width and near: %v", sortedKeys(r.Pseudo))
	}
}

func paramByName(ps []*api.Parameter, name string) *api.Parameter {
	for _, p := range ps {
		if p.Name == name {
			return p
		}
	}
	return nil
}
