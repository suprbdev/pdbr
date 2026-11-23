package parse

import (
	"context"
	"errors"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"
	"github.com/suprbdev/pdbcore/testutil"

	"github.com/suprbdev/pdbr/internal/api"
	"github.com/suprbdev/pdbr/internal/compile"
	"github.com/suprbdev/pdbr/internal/inflect"
	"github.com/suprbdev/pdbr/internal/plan"
	postgisplugin "github.com/suprbdev/pdbr/internal/plugins/postgis"
	"github.com/suprbdev/pdbr/internal/testcatalog"
)

// buildGeo builds the fixture plus testcatalog.Contrib and testcatalog.Geo
// (cube, earth, point and raster columns) with the postgis plugin applied,
// allowing kb.docs.body, kb.sites.ll and kb.tiles.rast under
// filters.indexed_only.
func buildGeo(t testing.TB, indexedOnly bool) *api.Built {
	t.Helper()
	b := api.New(testcatalog.Geo(testcatalog.Contrib(testutil.FixtureCatalog())), inflect.Default, api.Options{
		FilterIndexedOnly: indexedOnly,
		AllowColumns: map[string][]string{
			"public.posts":                       {"published"},
			testcatalog.ContribSchema + ".docs":  {"body"},
			testcatalog.ContribSchema + ".sites": {"ll"},
			testcatalog.ContribSchema + ".tiles": {"rast"},
		},
		Functions: true, BasePath: "/api", Schemas: []string{"public", testcatalog.ContribSchema},
		MaxPageSize: 100, Etags: true, Version: "test", Logger: quiet(),
	})
	if err := postgisplugin.New(nil).TransformAPI(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	return built
}

func parseGeo(t *testing.T, built *api.Built, target string) (*plan.Request, error) {
	t.Helper()
	r := httptest.NewRequest("GET", "http://x/api"+target, nil)
	path, _, _ := cutQuery(target)
	m, ok := built.Match(path)
	if !ok {
		t.Fatalf("no route for %s", path)
	}
	return Parse(built, m, r, allOn)
}

func cutQuery(target string) (string, string, bool) {
	for i := range target {
		if target[i] == '?' {
			return target[:i], target[i+1:], true
		}
	}
	return target, "", false
}

// TestCubeOperands: cube cs/cd/ov take a JSON point, a JSON box or the cube
// text form; vec= is a cube reference for the cube distances; generic
// comparisons, plain ordering and distinct are refused.
func TestCubeOperands(t *testing.T) {
	built := buildGeo(t, true)
	p, err := parseGeo(t, built, "/kb/shapes?box=cs.%5B1%2C2%5D&order=box.vec_chebyshev&vec=%5B%5B0%2C0%5D%2C%5B1%2C1%5D%5D")
	if err != nil {
		t.Fatal(err)
	}
	c := p.Filter.(plan.Cond)
	if c.Op != plan.OpCs || len(c.Value.([]any)) != 2 {
		t.Errorf("cs operand = %#v", c.Value)
	}
	if co := p.Order[0].Contrib; co == nil || co.Expr != "chebyshevDistance" {
		t.Errorf("order = %+v", p.Order[0])
	}
	if v, ok := p.Vec.([]any); !ok || len(v) != 2 {
		t.Errorf("vec = %#v", p.Vec)
	}
	p, err = parseGeo(t, built, "/kb/shapes?box=ov.(0,0),(1,1)")
	if err != nil || p.Filter.(plan.Cond).Value != "(0,0),(1,1)" {
		t.Fatalf("text operand: %v %+v", err, p)
	}
	for target, code := range map[string]string{
		"/kb/shapes?box=eq.(1,2)":                       CodeInvalidFilter,
		"/kb/shapes?box=gt.(1,2)":                       CodeInvalidFilter,
		"/kb/shapes?box=cs.%5B%5D":                      CodeInvalidFilter,
		"/kb/shapes?box=cs.%5B1":                        CodeInvalidFilter,
		"/kb/shapes?order=box":                          CodeColumnNotOrderable,
		"/kb/shapes?distinct=box":                       CodeInvalidFilter,
		"/kb/shapes?order=box.vec_l2":                   CodeInvalidFilter,
		"/kb/shapes?order=box.vec_ip&vec=%5B1%5D":       CodeColumnNotOrderable,
		"/kb/shapes?order=pt.vec_l2&vec=%5B1%5D":        CodeColumnNotOrderable,
		"/kb/shapes?box=vec_l2_lt.x&vec=%5B1%5D":        CodeInvalidFilter,
		"/kb/shapes?box=vec_l2_lt.1":                    CodeInvalidFilter,
		"/kb/shapes?vec=%5B1%5D":                        CodeInvalidFilter,
		"/kb/shapes?order=box.vec_l2&vec=%5B%22a%22%5D": CodeInvalidFilter,
	} {
		if _, err := parseGeo(t, built, target); err == nil {
			t.Errorf("%s: no error", target)
		} else {
			var ue *compile.UserError
			if !errors.As(err, &ue) || ue.Code != code {
				t.Errorf("%s: %v, want %s", target, err, code)
			}
		}
	}
	aggs := allOn
	aggs.Aggregates = true
	m, _ := built.Match("/kb/shapes")
	_, err = Parse(built, m, httptest.NewRequest("GET", "http://x/api/kb/shapes?select=box.max()", nil), aggs)
	wantCode(t, err, CodeInvalidSelect)
	if _, err := parseGeo(t, buildGeo(t, false), "/kb/shapes?order=pt.vec_l2&pt=vec_l1_lt.2&vec=%5B1%5D"); err != nil {
		t.Errorf("unindexed cube column under indexed_only off: %v", err)
	}
}

// TestLatLon: latlon=<lat>,<lon> becomes the {lat, lon} reference of the
// earthdistance order terms, scores and thresholds, is validated as a
// location, and is required by (and only accepted with) one of them.
func TestLatLon(t *testing.T) {
	built := buildGeo(t, true)
	p, err := parseGeo(t, built, "/kb/sites?select=id,loc_earth_distance&loc=earth_lt.1000&order=loc.earth_dist.desc&latlon=51.5,-0.12")
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]any{"lat": 51.5, "lon": -0.12}; !reflect.DeepEqual(p.LatLon, want) {
		t.Errorf("latlon = %#v", p.LatLon)
	}
	if c := p.Filter.(plan.Cond); c.Value != 1000.0 {
		t.Errorf("earth_lt operand = %#v", c.Value)
	}
	if _, err := parseGeo(t, built, "/kb/sites?ll=earth_miles_lt.3&order=ll.earth_miles&latlon=0,0"); err != nil {
		t.Errorf("allowlisted point column: %v", err)
	}
	for target, code := range map[string]string{
		"/kb/sites?order=loc.earth_dist":                      CodeInvalidFilter,
		"/kb/sites?latlon=1,2":                                CodeInvalidFilter,
		"/kb/sites?order=loc.earth_dist&latlon=1":             CodeInvalidFilter,
		"/kb/sites?order=loc.earth_dist&latlon=1,2,3":         CodeInvalidFilter,
		"/kb/sites?order=loc.earth_dist&latlon=x,2":           CodeInvalidFilter,
		"/kb/sites?order=loc.earth_dist&latlon=0,181":         CodeInvalidFilter,
		"/kb/sites?order=loc.earth_dist&latlon=NaN,0":         CodeInvalidFilter,
		"/kb/sites?order=loc":                                 CodeColumnNotOrderable,
		"/kb/sites?loc=eq.(1,2,3)":                            CodeInvalidFilter,
		"/kb/sites?loc=earth_lt.inf&latlon=0,0":               CodeInvalidFilter,
		"/kb/sites?select=ll_earth_distance_miles":            CodeInvalidFilter,
		"/kb/sites?order=loc.earth_miles&latlon=0,0":          CodeColumnNotOrderable,
		"/kb/sites?order=name.earth_dist&latlon=0,0":          CodeColumnNotOrderable,
		"/kb/sites?select=loc_earth_distance(x=1)&latlon=0,0": CodeInvalidSelect,
	} {
		_, err := parseGeo(t, built, target)
		var ue *compile.UserError
		if !errors.As(err, &ue) || ue.Code != code {
			t.Errorf("%s: %v, want %s", target, err, code)
		}
	}
	unindexed := buildCatalog(t, testcatalog.Geo(testcatalog.Contrib(testutil.FixtureCatalog())), true, []string{"public", testcatalog.ContribSchema}, nil)
	if _, err := parseGeo(t, unindexed, "/kb/sites?order=ll.earth_miles&latlon=0,0"); err == nil {
		t.Error("point column outside filters.allow_columns orders by <@> under indexed_only")
	}
	if unindexed.ByPath["kb.sites"].Pseudo["ll_earth_distance_miles"] != nil {
		t.Error("point column outside filters.allow_columns has the miles score under indexed_only")
	}
}

// TestLatLonYieldsToColumn: a column named latlon takes the parameter's
// name, so it is a filter there and the earthdistance surface needing it
// is gone.
func TestLatLonYieldsToColumn(t *testing.T) {
	cat := testcatalog.Geo(testcatalog.Contrib(testutil.FixtureCatalog()))
	sites := cat.Table(testcatalog.ContribSchema, "sites")
	sites.Columns = append(sites.Columns, &introspect.Column{Name: "latlon", Position: 5, PGType: "text", TypeSchema: "pg_catalog"})
	built := buildCatalog(t, cat, false, []string{"public", testcatalog.ContribSchema}, nil)
	p, err := parseGeo(t, built, "/kb/sites?latlon=eq.x")
	if err != nil || p.Filter.(plan.Cond).Column.Name != "latlon" {
		t.Fatalf("latlon filter: %v %+v", err, p)
	}
	_, err = parseGeo(t, built, "/kb/sites?order=loc.earth_dist")
	wantCode(t, err, CodeColumnNotOrderable)
	_, err = parseGeo(t, built, "/kb/sites?loc=earth_lt.5")
	wantCode(t, err, CodeColumnNotFilterable)
	if built.ByPath["kb.sites"].Pseudo["loc_earth_distance"] != nil {
		t.Error("earth score registered where latlon is a column")
	}
}

// TestRasterSurface: a raster column is not readable (400 invalid_select,
// absent from *), not orderable, filtered by intersects and is.null only,
// and its pseudo-columns take band= (value and statistics) with near=
// required by the value.
func TestRasterSurface(t *testing.T) {
	built := buildGeo(t, true)
	tiles := built.ByPath["kb.tiles"]
	if tiles.Field("rast") != nil || tiles.Raster("rast") == nil {
		t.Fatal("raster column is readable")
	}
	p, err := parseGeo(t, built, "/kb/tiles?select=id,rast_width,v:rast_value(band=2),rast_summary_stats&near=POINT(1%202)&rast=intersects.SRID%3D4326%3BPOINT(1%202)")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Select.Fields[2].Args["band"]; got != int64(2) {
		t.Errorf("band = %#v", got)
	}
	c := p.Filter.(plan.Cond)
	if _, ok := c.Value.(postgis.Value); !ok || c.Op != plan.OpRasterIntersects {
		t.Errorf("intersects cond = %+v", c)
	}
	if p, err := parseGeo(t, built, "/kb/tiles?select=rast_envelope(precision=3)"); err != nil || p.Select.Fields[0].Args["precision"] != int64(3) {
		t.Errorf("envelope precision: %v %+v", err, p)
	}
	if _, err := parseGeo(t, built, "/kb/tiles?rast=not.is.null&or=(rast.intersects.POINT(0%200),id.eq.1)"); err != nil {
		t.Errorf("is.null / logic intersects: %v", err)
	}
	for target, code := range map[string]string{
		"/kb/tiles?select=rast":                                        CodeInvalidSelect,
		"/kb/tiles?select=*,rast":                                      CodeInvalidSelect,
		"/kb/tiles?select=history":                                     CodeInvalidSelect,
		"/kb/tiles?order=rast":                                         CodeColumnNotOrderable,
		"/kb/tiles?distinct=rast":                                      CodeUnknownColumn,
		"/kb/tiles?rast=eq.x":                                          CodeInvalidFilter,
		"/kb/tiles?rast=is.true":                                       CodeInvalidFilter,
		"/kb/tiles?rast=st_intersects.POINT(0%200)":                    CodeInvalidFilter,
		"/kb/tiles?rast=intersects.junk":                               CodeInvalidFilter,
		"/kb/tiles?history=is.null":                                    CodeColumnNotFilterable,
		"/kb/tiles?name=intersects.POINT(0%200)":                       CodeColumnNotFilterable,
		"/kb/tiles?select=rast_value":                                  CodeInvalidFilter,
		"/kb/tiles?select=rast_value(band=0)&near=POINT(0%200)":        CodeInvalidSelect,
		"/kb/tiles?select=rast_value(band=x)&near=POINT(0%200)":        CodeInvalidSelect,
		"/kb/tiles?select=rast_value(spheroid=true)&near=POINT(0%200)": CodeInvalidSelect,
		"/kb/tiles?select=rast_envelope(precision=16)":                 CodeInvalidSelect,
		"/kb/tiles?select=rast_envelope(transform=3857)":               CodeInvalidSelect,
		"/kb/tiles?select=rast_width(precision=2)":                     CodeInvalidSelect,
		"/kb/tiles?select=rast_srid(band=1)":                           CodeInvalidSelect,
		"/kb/tiles?near=POINT(0%200)":                                  CodeInvalidFilter,
		"/places?location=intersects.POINT(0%200)":                     CodeInvalidFilter,
	} {
		_, err := parseGeo(t, built, target)
		var ue *compile.UserError
		if !errors.As(err, &ue) || ue.Code != code {
			t.Errorf("%s: %v, want %s", target, err, code)
		}
	}
	unlisted := buildCatalog(t, testcatalog.Geo(testcatalog.Contrib(testutil.FixtureCatalog())), true, []string{"public", testcatalog.ContribSchema}, nil)
	m, _ := unlisted.Match("/kb/tiles")
	_, err = Parse(unlisted, m, httptest.NewRequest("GET", "http://x/api/kb/tiles?rast=is.null", nil), allOn)
	wantCode(t, err, CodeColumnNotFilterable)
	_, err = Parse(unlisted, m, httptest.NewRequest("GET", "http://x/api/kb/tiles?select=rast_width", nil), allOn)
	wantCode(t, err, CodeUnknownColumn)
}
