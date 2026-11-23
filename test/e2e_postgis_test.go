package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbr/internal/config"
)

// Fixture geometry: place 1 Trafalgar Square (-0.1281 51.508, the only row
// with an area polygon), 2 British Museum (-0.1270 51.5194), 3 Greenwich
// (-0.0005 51.4779), 4 Sydney Opera House (151.2153 -33.8568).
const (
	trafalgar     = "SRID=4326;POINT(-0.1281 51.508)"
	trafalgarArea = "SRID=4326;POLYGON((-0.130 51.507, -0.126 51.507, -0.126 51.509, -0.130 51.509, -0.130 51.507))"
	londonPolygon = `{"type":"Polygon","coordinates":[[[-0.5,51.3],[0.3,51.3],[0.3,51.7],[-0.5,51.7],[-0.5,51.3]]]}`
)

// q builds /api/places?<params> with every value percent-encoded the way a
// client would (spaces, quotes, braces), keeping the test lines readable.
func q(srv string, params ...string) string {
	var sb strings.Builder
	sb.WriteString(srv + "/api/places?")
	for i, p := range params {
		if i > 0 {
			sb.WriteByte('&')
		}
		k, v, _ := strings.Cut(p, "=")
		sb.WriteString(k + "=" + url.QueryEscape(v))
	}
	return sb.String()
}

func TestPostGISFilters(t *testing.T) {
	srv := testServer(t, nil)
	cases := []struct {
		params []string
		want   []int
	}{
		{[]string{"select=id", "location=st_dwithin.(\"" + trafalgar + "\",5000,true)", "order=id"}, []int{1, 2}},
		{[]string{"select=id", "location=st_dwithin.(\"" + trafalgar + "\",10000,true)", "order=id"}, []int{1, 2, 3}},
		{[]string{"select=id", "location=st_dwithin.(\"" + trafalgar + "\",0.05)", "order=id"}, []int{1, 2}}, // degrees on geometry
		{[]string{"select=id", "location=st_beyond.(\"" + trafalgar + "\",100000,true)"}, []int{4}},
		{[]string{"select=id", "location=not.st_dwithin.(\"" + trafalgar + "\",100000,true)"}, []int{4}},
		{[]string{"select=id", "area=st_dwithin.(\"POINT(-0.128 51.508)\",1)"}, []int{1}}, // metres on geography
		{[]string{"select=id", "location=bbox_intersects.(-0.5,51.3,0.3,51.7)", "order=id"}, []int{1, 2, 3}},
		{[]string{"select=id", "location=bbox_containedby.(-0.5,51.3,0.3,51.7)", "order=id"}, []int{1, 2, 3}},
		{[]string{"select=id", "location=bbox_contains.(-0.1281,51.508,-0.1281,51.508)"}, []int{1}},
		{[]string{"select=id", "location=bbox_intersects.(-0.5,51.3,0.3,51.7,4326)", "order=id"}, []int{1, 2, 3}},
		{[]string{"select=id", "location=not.bbox_intersects.(-0.5,51.3,0.3,51.7)"}, []int{4}},
		{[]string{"select=id", "area=bbox_intersects.(-0.129,51.508,-0.128,51.5085)"}, []int{1}},
		{[]string{"select=id", "area=bbox_contains.(-0.129,51.508,-0.128,51.5085)"}, []int{1}}, // ~ has no geography overload
		{[]string{"select=id", "location=st_intersects." + trafalgar}, []int{1}},
		{[]string{"select=id", "location=st_equals." + trafalgar}, []int{1}},
		{[]string{"select=id", "location=st_disjoint." + trafalgar, "order=id"}, []int{2, 3, 4}},
		{[]string{"select=id", "location=st_within." + londonPolygon, "order=id"}, []int{1, 2, 3}},
		{[]string{"select=id", "location=not.st_within." + londonPolygon}, []int{4}},
		{[]string{"select=id", "location=st_coveredby." + londonPolygon, "order=id"}, []int{1, 2, 3}},
		{[]string{"select=id", "area=st_covers.SRID=4326;POINT(-0.128 51.508)"}, []int{1}},
		{[]string{"select=id", "or=(area.st_covers.\"SRID=4326;POINT(-0.128 51.508)\",id.eq.4)", "order=id"}, []int{1, 4}},
		{[]string{"select=id", "area=st_covers." + `{"type":"Point","coordinates":[-0.128,51.508]}`}, []int{1}},
		{[]string{"select=id", "area=st_intersects." + `{"type":"Feature","geometry":{"type":"Point","coordinates":[-0.128,51.508]}}`}, []int{1}},
		{[]string{"select=id", "area=st_contains.POINT(-0.128 51.508)"}, []int{1}}, // geometry-only predicate on geography
		{[]string{"select=id", "area=st_containsproperly.POINT(-0.128 51.508)"}, []int{1}},
		{[]string{"select=id", "area=st_within." + londonPolygon}, []int{1}},
		{[]string{"select=id", "area=st_coveredby." + londonPolygon}, []int{1}},
		{[]string{"select=id", "area=st_overlaps." + londonPolygon}, nil},
		{[]string{"select=id", "area=st_touches.POINT(-0.130 51.507)"}, []int{1}},
		{[]string{"select=id", "area=st_crosses.LINESTRING(-0.2 51.508, 0 51.508)"}, []int{1}},
		{[]string{"select=id", "area=is.null", "order=id"}, []int{2, 3, 4}},
		{[]string{"select=id", "area=not.is.null"}, []int{1}},
		{[]string{"select=id", "or=(location.st_dwithin.(\"SRID=4326;POINT(151.2153 -33.8568)\",1,true),id.eq.1)", "order=id"}, []int{1, 4}},
		{[]string{"select=id", "and=(location.bbox_intersects.(-0.5,51.3,0.3,51.7),not.location.st_equals.\"" + trafalgar + "\")", "order=id"}, []int{2, 3}},
		// GeoJSON inside a logic list: quoted, inner quotes escaped.
		{[]string{"select=id", "not.and=(location.st_within.\"" + strings.ReplaceAll(londonPolygon, `"`, `\"`) + "\",area.is.null)", "order=id"}, []int{1, 4}},
		{[]string{"select=id", "location=bbox_leftof.SRID=4326;POINT(0 0)", "order=id"}, []int{1, 2, 3}},
		{[]string{"select=id", "location=bbox_rightof.SRID=4326;POINT(0 0)"}, []int{4}},
		{[]string{"select=id", "location=bbox_below.SRID=4326;POINT(0 51.5)", "order=id"}, []int{3, 4}},
		{[]string{"select=id", "location=bbox_above.SRID=4326;POINT(0 51.5)", "order=id"}, []int{1, 2}},
		{[]string{"select=id", "location=bbox_overlapsorleftof.SRID=4326;POINT(-0.127 0)", "order=id"}, []int{1, 2}},
		{[]string{"select=id", "location=bbox_overlapsorrightof.SRID=4326;POINT(-0.127 0)", "order=id"}, []int{2, 3, 4}},
		{[]string{"select=id", "location=bbox_overlapsorbelow.SRID=4326;POINT(0 51.508)", "order=id"}, []int{1, 3, 4}},
		{[]string{"select=id", "location=bbox_overlapsorabove.SRID=4326;POINT(0 51.508)", "order=id"}, []int{1, 2}},
		{[]string{"select=id", "location=not.bbox_leftof.SRID=4326;POINT(0 0)"}, []int{4}},
		{[]string{"select=id", "location=bbox_equals." + trafalgar}, []int{1}},
		{[]string{"select=id", "location=st_exactlyequals." + trafalgar}, []int{1}},
		{[]string{"select=id", "location=st_exactlyequals." + `{"type":"Point","coordinates":[-0.1281,51.508]}`}, []int{1}},
		{[]string{"select=id", "location=st_orderingequals." + trafalgar}, []int{1}},
		{[]string{"select=id", "location=st_intersects3d." + trafalgar}, []int{1}},
		{[]string{"select=id", "location=bbox_intersectsnd." + trafalgar}, []int{1}},
		{[]string{"select=id", "location=not.st_exactlyequals." + trafalgar, "order=id"}, []int{2, 3, 4}},
		{[]string{"select=id", "or=(location.bbox_rightof.\"SRID=4326;POINT(0 0)\",location.st_exactlyequals.\"" + trafalgar + "\")", "order=id"}, []int{1, 4}},
		{[]string{"select=id", "area=bbox_leftof.POINT(0 51.508)"}, []int{1}},
		{[]string{"select=id", "area=bbox_above.POINT(0 51.5)"}, []int{1}},
		{[]string{"select=id", "area=bbox_below.POINT(0 51.5)"}, nil},
		{[]string{"select=id", "area=bbox_overlapsorrightof.POINT(-0.131 51.508)"}, []int{1}},
		{[]string{"select=id", "area=st_exactlyequals." + trafalgarArea}, []int{1}},
		{[]string{"select=id", "area=st_orderingequals." + trafalgarArea}, []int{1}},
		{[]string{"select=id", "area=st_orderingequals.SRID=4326;POLYGON((-0.126 51.507, -0.126 51.509, -0.130 51.509, -0.130 51.507, -0.126 51.507))"}, nil},
		{[]string{"select=id", "area=st_equals.SRID=4326;POLYGON((-0.126 51.507, -0.126 51.509, -0.130 51.509, -0.130 51.507, -0.126 51.507))"}, []int{1}},
		{[]string{"select=id", "area=bbox_equals." + trafalgarArea}, []int{1}},
		{[]string{"select=id", "area=st_intersects3d.POINT(-0.128 51.508)"}, []int{1}},
		{[]string{"select=id", "area=bbox_intersectsnd.POINT(-0.128 51.508)"}, []int{1}},
		{[]string{"select=id", "location=bbox_intersects2d.SRID=4326;LINESTRING(-0.2 51.5,-0.1 51.51)"}, []int{1}},
		{[]string{"select=id", "area=bbox_intersects2d.POINT(-0.128 51.508)"}, []int{1}},
		{[]string{"select=id", "area=not.bbox_intersects2d.POINT(10 10)"}, []int{1}},
		{[]string{"select=id", "or=(area.bbox_intersects2d.\"POINT(10 10)\",location.bbox_intersects2d.\"" + trafalgar + "\")"}, []int{1}},
		{[]string{"select=id", "location=st_intersects.POINT(-0.1281 51.508)"}, []int{1}},
		{[]string{"select=id", "location=st_dwithin.(\"POINT(-0.1281 51.508)\",0.05)", "order=id"}, []int{1, 2}},
		{[]string{"select=id", "location=bbox_leftof.POINT(0 0)", "order=id"}, []int{1, 2, 3}},
	}
	for _, tc := range cases {
		var rows []row
		getJSON(t, q(srv.URL, tc.params...), &rows)
		if got := ids(rows, "id"); !eq(got, tc.want) {
			t.Errorf("%v = %v, want %v", tc.params, got, tc.want)
		}
	}
	bad := []struct {
		params []string
		code   string
	}{
		{[]string{"location=eq.POINT(1 2)"}, "invalid_filter"},
		{[]string{"location=in.(POINT(1 2))"}, "invalid_filter"},
		{[]string{"location=lt.1"}, "invalid_filter"},
		{[]string{"location=st_intersects.not a geometry"}, "invalid_filter"},
		{[]string{"location=st_intersects." + `{"type":"Nope","coordinates":[1,2]}`}, "invalid_filter"},
		{[]string{"location=st_dwithin.(POINT(1 2))"}, "invalid_filter"},
		{[]string{"location=st_dwithin.(\"POINT(1 2)\",abc)"}, "invalid_filter"},
		{[]string{"location=st_dwithin.(\"POINT(1 2)\",1,maybe)"}, "invalid_filter"},
		{[]string{"location=bbox_intersects.(1,2,3)"}, "invalid_filter"},
		{[]string{"location=bbox_intersects.(1,2,3,x)"}, "invalid_filter"},
		{[]string{"owner_id=st_intersects.POINT(1 2)"}, "invalid_filter"},
		{[]string{"id=bbox_intersects.(1,2,3,4)"}, "invalid_filter"},
		{[]string{"location=st_nope.POINT(1 2)"}, "invalid_filter"},
		{[]string{"location=bbox_leftof.(1,2,3,4)"}, "invalid_filter"},
		{[]string{"owner_id=st_exactlyequals.POINT(1 2)"}, "invalid_filter"},
		{[]string{"location=bbox_intersectsnd.not a geometry"}, "invalid_filter"},
		{[]string{"distinct=location", "select=id"}, "invalid_filter"},
	}
	for _, tc := range bad {
		res, body := get(t, q(srv.URL, tc.params...))
		if res.StatusCode != 400 {
			t.Errorf("%v = %d %s, want 400 %s", tc.params, res.StatusCode, body, tc.code)
			continue
		}
		wantProblem(t, res, body, 400, tc.code)
	}
}

func TestPostGISPseudoColumns(t *testing.T) {
	srv := testServer(t, nil)
	var rows []row
	getJSON(t, q(srv.URL, "select=name,location_srid,location_geometry_type,location_is_valid,location_centroid,location_envelope,location_area,location_length,sq_m:area_area,area_perimeter,area_length,area_centroid,area_envelope,area_srid,area_geometry_type,area_is_valid", "id=eq.1"), &rows)
	if len(rows) != 1 {
		t.Fatalf("rows = %v", rows)
	}
	r := rows[0]
	if r["location_srid"] != float64(4326) || r["location_geometry_type"] != "ST_Point" || r["location_is_valid"] != true {
		t.Errorf("location accessors = %v", r)
	}
	if c, _ := r["location_centroid"].(map[string]any); c["type"] != "Point" {
		t.Errorf("location_centroid = %v", r["location_centroid"])
	}
	if r["location_area"] != float64(0) || r["location_length"] != float64(0) {
		t.Errorf("point area/length = %v %v", r["location_area"], r["location_length"])
	}
	if sq, _ := r["sq_m"].(float64); sq < 50000 || sq > 80000 {
		t.Errorf("area_area (m²) = %v", r["sq_m"])
	}
	if per, _ := r["area_perimeter"].(float64); per < 900 || per > 1100 {
		t.Errorf("area_perimeter (m) = %v", r["area_perimeter"])
	}
	if c, _ := r["area_centroid"].(map[string]any); c["type"] != "Point" {
		t.Errorf("area_centroid = %v", r["area_centroid"])
	} else if xy, _ := c["coordinates"].([]any); len(xy) != 2 || math.Abs(xy[0].(float64)+0.128) > 0.001 || math.Abs(xy[1].(float64)-51.508) > 0.001 {
		t.Errorf("area_centroid = %v", c)
	}
	if e, _ := r["area_envelope"].(map[string]any); e["type"] != "Polygon" {
		t.Errorf("area_envelope = %v", r["area_envelope"])
	}
	if r["area_srid"] != float64(4326) || r["area_geometry_type"] != "ST_Polygon" || r["area_is_valid"] != true {
		t.Errorf("area accessors = %v", r)
	}
	// NULL geometry → NULL accessors.
	getJSON(t, q(srv.URL, "select=area_area,area_centroid", "id=eq.2"), &rows)
	if rows[0]["area_area"] != nil || rows[0]["area_centroid"] != nil {
		t.Errorf("null accessors = %v", rows[0])
	}
	// * never includes pseudo-columns.
	getJSON(t, q(srv.URL, "id=eq.1"), &rows)
	if _, ok := rows[0]["location_srid"]; ok {
		t.Errorf("* included a pseudo-column: %v", rows[0])
	}
	// Single-row route and embeds.
	var one row
	getJSON(t, srv.URL+"/api/places/1?select=name,location_srid", &one)
	if one["location_srid"] != float64(4326) {
		t.Errorf("row route pseudo = %v", one)
	}
	getJSON(t, srv.URL+"/api/users?select=id,places(id,location_srid)&id=eq.1", &rows)
	if pl, _ := rows[0]["places"].([]any); len(pl) != 2 || pl[0].(map[string]any)["location_srid"] != float64(4326) {
		t.Errorf("embedded pseudo = %v", rows[0])
	}
	// Not filterable, orderable or writable.
	for _, tc := range []struct{ params, code string }{
		{"location_srid=eq.4326", "unknown_column"},
		{"order=location_srid", "unknown_column"},
	} {
		res, body := get(t, srv.URL+"/api/places?"+tc.params)
		wantProblem(t, res, body, 400, tc.code)
	}
	res, body := send(t, "PATCH", srv.URL+"/api/places/1", `{"location_srid": 1}`, nil)
	wantProblem(t, res, body, 400, "invalid_body")
}

func TestPostGISAccessors(t *testing.T) {
	srv := testServer(t, nil)
	near := func(v any, want float64) bool {
		f, ok := v.(float64)
		return ok && math.Abs(f-want) < 1e-6
	}
	var rows []row
	getJSON(t, q(srv.URL, "select=location_x,location_y,location_num_points,location_num_geometries,location_point_on_surface,location_wkt,location_ewkt,location_is_empty,location_is_simple,location_xmin,location_ymin,location_xmax,location_ymax,mx:location_x(transform=3857),area_x,area_y,area_z,area_m,area_num_points,area_num_geometries,area_start_point,area_end_point,area_point_on_surface,area_wkt,area_ewkt,area_is_empty,area_is_simple,area_xmin,area_ymin,area_xmax,area_ymax", "id=eq.1"), &rows)
	if len(rows) != 1 {
		t.Fatalf("rows = %v", rows)
	}
	r := rows[0]
	if mx, _ := r["mx"].(float64); !near(r["location_x"], -0.1281) || !near(r["location_y"], 51.508) || math.Abs(mx+14260.03) > 1 {
		t.Errorf("location x/y = %v %v %v", r["location_x"], r["location_y"], r["mx"])
	}
	if r["location_num_points"] != float64(1) || r["location_num_geometries"] != float64(1) || r["location_is_empty"] != false || r["location_is_simple"] != true {
		t.Errorf("location counts/predicates = %v", r)
	}
	if r["location_wkt"] != "POINT(-0.1281 51.508)" || r["location_ewkt"] != trafalgar {
		t.Errorf("location wkt/ewkt = %q %q", r["location_wkt"], r["location_ewkt"])
	}
	if p, _ := r["location_point_on_surface"].(map[string]any); p["type"] != "Point" {
		t.Errorf("location_point_on_surface = %v", r["location_point_on_surface"])
	}
	if !near(r["location_xmin"], -0.1281) || !near(r["location_xmax"], -0.1281) || !near(r["location_ymin"], 51.508) || !near(r["location_ymax"], 51.508) {
		t.Errorf("location extent = %v %v %v %v", r["location_xmin"], r["location_ymin"], r["location_xmax"], r["location_ymax"])
	}
	for _, k := range []string{"area_x", "area_y", "area_z", "area_m", "area_start_point", "area_end_point"} {
		if r[k] != nil {
			t.Errorf("%s on a polygon = %v, want null", k, r[k])
		}
	}
	if r["area_num_points"] != float64(5) || r["area_num_geometries"] != float64(1) || r["area_is_empty"] != false || r["area_is_simple"] != true {
		t.Errorf("area counts/predicates = %v", r)
	}
	if w, _ := r["area_wkt"].(string); !strings.HasPrefix(w, "POLYGON((") {
		t.Errorf("area_wkt = %v", r["area_wkt"])
	}
	if w, _ := r["area_ewkt"].(string); !strings.HasPrefix(w, "SRID=4326;POLYGON((") {
		t.Errorf("area_ewkt = %v", r["area_ewkt"])
	}
	if p, _ := r["area_point_on_surface"].(map[string]any); p["type"] != "Point" {
		t.Errorf("area_point_on_surface = %v", r["area_point_on_surface"])
	}
	extent := func(v any, want float64) bool {
		f, ok := v.(float64)
		return ok && math.Abs(f-want) < 1e-5
	}
	if !extent(r["area_xmin"], -0.130) || !extent(r["area_ymin"], 51.507) || !extent(r["area_xmax"], -0.126) || !extent(r["area_ymax"], 51.509) {
		t.Errorf("area extent = %v %v %v %v", r["area_xmin"], r["area_ymin"], r["area_xmax"], r["area_ymax"])
	}
	getJSON(t, q(srv.URL, "select=area_x,area_wkt,area_is_empty", "id=eq.2"), &rows)
	if rows[0]["area_x"] != nil || rows[0]["area_wkt"] != nil || rows[0]["area_is_empty"] != nil {
		t.Errorf("null geometry accessors = %v", rows[0])
	}

	for _, key := range []string{"location_z", "location_m", "location_start_point", "location_end_point"} {
		res, body := get(t, q(srv.URL, "select=id,"+key))
		wantProblem(t, res, body, 400, "unknown_column")
	}
	props := schemas(openAPI(t, srv.URL))["places"].(map[string]any)["properties"].(map[string]any)
	for _, key := range []string{"location_z", "location_m", "location_start_point", "location_end_point"} {
		if _, ok := props[key]; ok {
			t.Errorf("OpenAPI advertises %s on a 2D Point column", key)
		}
	}
	for _, key := range []string{"location_x", "location_y", "location_wkt", "area_z", "area_m", "area_start_point", "area_end_point", "area_ymax"} {
		if p, _ := props[key].(map[string]any); p == nil || p["readOnly"] != true {
			t.Errorf("OpenAPI %s = %v", key, props[key])
		}
	}

	sweep(t)
	var pl row
	sendJSON(t, "POST", srv.URL+"/api/places?select=id,area_geometry_type,area_start_point,area_end_point,area_num_points,area_x", `{"name": "M3 postgis line area", "area": "SRID=4326;LINESTRING(0 0, 1 1, 2 0)"}`, 201, &pl)
	defer sendJSON(t, "DELETE", fmt.Sprintf("%s/api/places/%d", srv.URL, rowID(t, pl)), "", 200, nil)
	sp, _ := pl["area_start_point"].(map[string]any)
	ep, _ := pl["area_end_point"].(map[string]any)
	if pl["area_geometry_type"] != "ST_LineString" || sp["type"] != "Point" || fmtAny(ep["coordinates"]) != "[2,0]" || pl["area_num_points"] != float64(3) || pl["area_x"] != nil {
		t.Errorf("linestring area accessors = %v", pl)
	}
	var pt row
	sendJSON(t, "POST", srv.URL+"/api/places?select=id,area_x,area_y,area_z,area_start_point", `{"name": "M3 postgis point area", "area": "SRID=4326;POINT(3 4)"}`, 201, &pt)
	defer sendJSON(t, "DELETE", fmt.Sprintf("%s/api/places/%d", srv.URL, rowID(t, pt)), "", 200, nil)
	if pt["area_x"] != float64(3) || pt["area_y"] != float64(4) || pt["area_z"] != nil || pt["area_start_point"] != nil {
		t.Errorf("point area accessors = %v", pt)
	}
}

func TestPostGISProjection(t *testing.T) {
	srv := testServer(t, nil)
	var rows []row
	getJSON(t, q(srv.URL, "select=name,location,web_mercator:location(transform=3857),coarse:location(simplify=0.01),both:area(transform=3857,simplify=0.5),c3857:area_centroid(transform=3857)", "id=eq.1"), &rows)
	r := rows[0]
	loc := r["location"].(map[string]any)["coordinates"].([]any)
	wm := r["web_mercator"].(map[string]any)["coordinates"].([]any)
	if math.Abs(loc[0].(float64)+0.1281) > 1e-6 || math.Abs(wm[0].(float64)+14260.03) > 1 || math.Abs(wm[1].(float64)-6711649.79) > 1 {
		t.Errorf("transform: %v -> %v", loc, wm)
	}
	if c, _ := r["coarse"].(map[string]any); c["type"] != "Point" {
		t.Errorf("simplify = %v", r["coarse"])
	}
	if b, _ := r["both"].(map[string]any); b["type"] != "Polygon" {
		t.Errorf("both = %v", r["both"])
	}
	if c, _ := r["c3857"].(map[string]any); c["type"] != "Point" || math.Abs(c["coordinates"].([]any)[1].(float64)-6711600) > 500 {
		t.Errorf("c3857 = %v", r["c3857"])
	}
	for _, sel := range []string{
		"location(foo=1)", "location(transform=abc)", "location(transform=-1)", "location(simplify=-2)",
		"name(transform=3857)", "location(transform=3857,extra=1)", "location()",
	} {
		res, body := get(t, q(srv.URL, "select="+sel))
		wantProblem(t, res, body, 400, "invalid_select")
	}
}

func TestPostGISPrecisionAndBuffer(t *testing.T) {
	srv := testServer(t, nil)
	var rows []row
	getJSON(t, q(srv.URL, "select=p2:location(precision=2),p0:location(precision=0),ring:location(buffer=0.01,precision=3),"+
		"geo:area(buffer=100),wm:area(buffer=100,transform=3857,precision=1),c:area_centroid(buffer=100,precision=4),"+
		"m2:area_area(buffer=100),plain:area_area", "id=eq.1"), &rows)
	r := rows[0]
	if got := mustJSON(t, r["p2"].(map[string]any)["coordinates"]); string(got) != "[-0.13,51.51]" {
		t.Errorf("precision=2 coordinates %s", got)
	}
	if got := mustJSON(t, r["p0"].(map[string]any)["coordinates"]); string(got) != "[0,52]" {
		t.Errorf("precision=0 coordinates %s", got)
	}
	ring := r["ring"].(map[string]any)
	if ring["type"] != "Polygon" || !strings.Contains(string(mustJSON(t, ring["coordinates"])), "51.508") {
		t.Errorf("buffered point %v", ring)
	}
	if g, _ := r["geo"].(map[string]any); g["type"] != "Polygon" {
		t.Errorf("geography buffer %v", r["geo"])
	}
	if wm, _ := r["wm"].(map[string]any); wm["type"] != "Polygon" || !strings.Contains(string(mustJSON(t, wm["coordinates"])), "671") {
		t.Errorf("buffered, transformed geography %v", r["wm"])
	}
	if c, _ := r["c"].(map[string]any); c["type"] != "Point" {
		t.Errorf("buffered centroid %v", r["c"])
	}
	if m2, plain := r["m2"].(float64), r["plain"].(float64); m2 <= plain+math.Pi*100*100*0.5 {
		t.Errorf("a 100 m buffer should add area in m²: %v -> %v", plain, m2)
	}
	res, body := do(t, "GET", q(srv.URL, "select=id,location(precision=3)", "id=eq.1"), map[string]string{"Accept": "application/geo+json"})
	if res.StatusCode != 200 || !strings.Contains(string(body), "51.508") || strings.Contains(string(body), "51.5080") {
		t.Errorf("geo+json with precision: %d %s", res.StatusCode, body)
	}
	for _, sel := range []string{
		"location(precision=16)", "location(precision=-1)", "location(precision=1.5)", "location(buffer=abc)",
		"location(buffer=Inf)", "area_area(precision=2)", "name(buffer=1)",
	} {
		res, body := get(t, q(srv.URL, "select="+sel))
		wantProblem(t, res, body, 400, "invalid_select")
	}
}

func TestPostGISDistanceOrder(t *testing.T) {
	srv := testServer(t, nil)
	var rows []row
	getJSON(t, q(srv.URL, "select=id,name", "order=location.dist.asc", "near="+trafalgar, "limit=3"), &rows)
	if got := ids(rows, "id"); !eq(got, []int{1, 2, 3}) {
		t.Errorf("nearest = %v", got)
	}
	getJSON(t, q(srv.URL, "select=id", "order=location.dist.desc,id", "near="+`{"type":"Point","coordinates":[-0.1281,51.508]}`), &rows)
	if got := ids(rows, "id"); !eq(got, []int{4, 3, 2, 1}) {
		t.Errorf("farthest = %v", got)
	}
	// One near= serves the root and embed terms; area (geography) uses <->
	// in metres, NULLs last.
	getJSON(t, srv.URL+"/api/users?select=id,places(id)&places.order=location.dist.desc&near="+url.QueryEscape(trafalgar)+"&id=eq.1", &rows)
	if pl, _ := rows[0]["places"].([]any); len(pl) != 2 || pl[0].(map[string]any)["id"] != float64(2) {
		t.Errorf("embed dist order = %v", rows[0])
	}
	getJSON(t, q(srv.URL, "select=id", "order=area.dist.asc,id", "near="+trafalgar), &rows)
	if got := ids(rows, "id"); !eq(got, []int{1, 2, 3, 4}) {
		t.Errorf("geography dist = %v", got)
	}
	getJSON(t, q(srv.URL, "select=id", "order=location.dist.asc", "near=POINT(-0.1281 51.508)", "limit=3"), &rows)
	if got := ids(rows, "id"); !eq(got, []int{1, 2, 3}) {
		t.Errorf("bare WKT near = %v", got)
	}
	res, out := get(t, q(srv.URL, "select=id", "order=id", "near="+trafalgar, "limit=1"))
	wantProblem(t, res, out, 400, "invalid_filter")
	// Keyset paging keeps the distance order.
	res = getJSON(t, q(srv.URL, "select=id", "order=location.dist.asc", "near="+trafalgar, "limit=2"), &rows)
	next := res.Header.Get("X-Next-Cursor")
	if next == "" || !eq(ids(rows, "id"), []int{1, 2}) {
		t.Fatalf("page 1 = %v cursor=%q", ids(rows, "id"), next)
	}
	getJSON(t, q(srv.URL, "select=id", "order=location.dist.asc", "near="+trafalgar, "limit=2", "after="+next), &rows)
	if got := ids(rows, "id"); !eq(got, []int{3, 4}) {
		t.Errorf("page 2 = %v", got)
	}
	for _, tc := range []struct {
		params []string
		code   string
	}{
		{[]string{"order=location.dist.asc"}, "invalid_filter"},
		{[]string{"order=location.dist.asc", "near=garbage"}, "invalid_filter"},
		{[]string{"order=location.dist.asc", "near="}, "invalid_filter"},
		{[]string{"order=name.dist.asc", "near=" + trafalgar}, "invalid_filter"},
		{[]string{"order=location.asc"}, "column_not_orderable"},
		{[]string{"order=nope.dist.asc", "near=" + trafalgar}, "unknown_column"},
	} {
		res, body := get(t, q(srv.URL, tc.params...))
		wantProblem(t, res, body, 400, tc.code)
	}
}

func TestPostGISMutations(t *testing.T) {
	srv := testServer(t, nil)
	sweep(t)
	var pl row
	sendJSON(t, "POST", srv.URL+"/api/places?select=id,name,location,area,location_srid,area_area", `{"name": "M3 postgis geojson", "owner_id": 1,
		"location": {"type": "Point", "coordinates": [-0.1, 51.5]},
		"area": "SRID=4326;POLYGON((-0.130 51.507, -0.126 51.507, -0.126 51.509, -0.130 51.509, -0.130 51.507))"}`, 201, &pl)
	id := rowID(t, pl)
	defer sendJSON(t, "DELETE", fmt.Sprintf("%s/api/places/%d", srv.URL, id), "", 200, nil)
	if loc, _ := pl["location"].(map[string]any); loc["type"] != "Point" || pl["location_srid"] != float64(4326) {
		t.Errorf("POST GeoJSON round trip = %v", pl)
	}
	if a, _ := pl["area"].(map[string]any); a["type"] != "Polygon" || pl["area_area"].(float64) < 1 {
		t.Errorf("POST EWKT round trip = %v", pl)
	}
	// PATCH: Feature wrapper unwrapped; EWKT keeps its SRID; select projection.
	sendJSON(t, "PATCH", fmt.Sprintf("%s/api/places/%d?select=location,wm:location(transform=3857),location_srid", srv.URL, id),
		`{"location": {"type": "Feature", "properties": {}, "geometry": {"type": "Point", "coordinates": [-0.2, 51.6]}}}`, 200, &pl)
	if c := pl["location"].(map[string]any)["coordinates"].([]any); c[0] != -0.2 || pl["location_srid"] != float64(4326) || pl["wm"] == nil {
		t.Errorf("PATCH Feature = %v", pl)
	}
	sendJSON(t, "PATCH", fmt.Sprintf("%s/api/places/%d?select=location,area", srv.URL, id), `{"location": "SRID=4326;POINT(1 2)", "area": null}`, 200, &pl)
	if c := pl["location"].(map[string]any)["coordinates"].([]any); c[0] != float64(1) || pl["area"] != nil {
		t.Errorf("PATCH EWKT/null = %v", pl)
	}
	sendJSON(t, "PATCH", fmt.Sprintf("%s/api/places/%d?select=location_srid", srv.URL, id), `{"location": "POINT(3 4)"}`, 200, &pl)
	if pl["location_srid"] != float64(4326) {
		t.Errorf("bare WKT geometry(Point,4326) = %v", pl)
	}
	// The column's typmod SRID is enforced by PostgreSQL (22023 → 400).
	res, out := send(t, "PATCH", fmt.Sprintf("%s/api/places/%d", srv.URL, id), `{"location": "SRID=3857;POINT(1 2)"}`, nil)
	wantProblem(t, res, out, 400, "pg:22023")
	// Bare WKT into geography is tagged 4326.
	sendJSON(t, "PATCH", fmt.Sprintf("%s/api/places/%d?select=area_srid", srv.URL, id), `{"area": "POLYGON((0 0, 0 1, 1 1, 0 0))"}`, 200, &pl)
	if pl["area_srid"] != float64(4326) {
		t.Errorf("bare WKT geography = %v", pl)
	}
	// Bulk: GeoJSON and EWKT elements side by side; the column typmod
	// (Point, 4326) is enforced by PostgreSQL.
	var many []row
	sendJSON(t, "POST", srv.URL+"/api/places?select=id,location_geometry_type,location_srid", `[
		{"name": "M3 postgis geojson 2", "location": {"type": "Point", "coordinates": [1, 2]}},
		{"name": "M3 postgis wkt", "location": "SRID=4326;POINT(0 0)"}]`, 201, &many)
	for _, r := range many {
		defer sendJSON(t, "DELETE", fmt.Sprintf("%s/api/places/%d", srv.URL, rowID(t, r)), "", 200, nil)
		if r["location_geometry_type"] != "ST_Point" || r["location_srid"] != float64(4326) {
			t.Errorf("bulk spatial = %v", r)
		}
	}
	if len(many) != 2 {
		t.Errorf("bulk spatial = %v", many)
	}
	res, out = send(t, "POST", srv.URL+"/api/places", `{"name": "M3 postgis line", "location": "SRID=4326;LINESTRING(0 0, 1 1)"}`, nil)
	wantProblem(t, res, out, 400, "pg:22023")
	// Malformed values fail before SQL.
	for _, body := range []string{
		`{"name": "M3 bad", "location": "not wkt"}`,
		`{"name": "M3 bad", "location": 42}`,
		`{"name": "M3 bad", "location": {"type": "Nope"}}`,
		`{"name": "M3 bad", "location": {"coordinates": [1, 2]}}`,
	} {
		res, out = send(t, "POST", srv.URL+"/api/places", body, nil)
		wantProblem(t, res, out, 400, "invalid_body")
	}
}

func openAPI(t *testing.T, srv string) map[string]any {
	t.Helper()
	_, body := get(t, srv+"/api/openapi.json")
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("openapi: %v", err)
	}
	return doc
}

func schemas(doc map[string]any) map[string]any {
	return doc["components"].(map[string]any)["schemas"].(map[string]any)
}

func TestPostGISOpenAPI(t *testing.T) {
	srv := testServer(t, nil)
	doc := openAPI(t, srv.URL)
	sch := schemas(doc)
	geo, _ := sch["GeoJSON"].(map[string]any)
	if geo == nil || geo["type"] != "object" {
		t.Fatalf("GeoJSON schema = %v", geo)
	}
	props := sch["places"].(map[string]any)["properties"].(map[string]any)
	loc := props["location"].(map[string]any)
	if anyOf, _ := loc["anyOf"].([]any); len(anyOf) != 2 || anyOf[0].(map[string]any)["$ref"] != "#/components/schemas/GeoJSON" {
		t.Errorf("location schema = %v", loc)
	}
	srid, _ := props["location_srid"].(map[string]any)
	if srid == nil || srid["readOnly"] != true {
		t.Errorf("location_srid schema = %v", srid)
	}
	if cen, _ := props["area_centroid"].(map[string]any); cen == nil || cen["readOnly"] != true {
		t.Errorf("area_centroid schema = %v", cen)
	}
	if _, ok := sch["users"].(map[string]any)["properties"].(map[string]any)["email_srid"]; ok {
		t.Error("pseudo-column on a non-spatial resource")
	}
	params := opParams(pathOp(doc, "/places", "get"))
	for _, col := range []string{"location", "area"} {
		ops, _ := params["query:"+col]["x-pdbr-filter-ops"].([]any)
		if len(ops) != 31 || ops[len(ops)-1] != "is" {
			t.Errorf("%s x-pdbr-filter-ops = %v", col, ops)
		}
		for _, op := range ops {
			var operand string
			switch op {
			case "is":
				operand = "is.null"
			case "st_dwithin", "st_beyond":
				operand = op.(string) + `.("SRID=4326;POINT(0 0)",1)`
			case "bbox_intersects", "bbox_contains", "bbox_containedby":
				operand = op.(string) + ".(0,0,1,1)"
			default:
				operand = op.(string) + ".SRID=4326;POINT(0 0)"
			}
			if res, body := get(t, q(srv.URL, "select=id", col+"="+operand)); res.StatusCode != 200 {
				t.Errorf("%s=%s = %d %s", col, operand, res.StatusCode, body)
			}
		}
	}
}

func TestPostGISDisabledAndSettings(t *testing.T) {
	spatialFilter := []string{"select=id", "location=st_intersects." + trafalgar}
	pseudo := []string{"select=id,location_srid"}
	dist := []string{"select=id", "order=location.dist.asc", "near=" + trafalgar}
	proj := []string{"select=location(transform=3857)"}
	expect := func(t *testing.T, srv string, params []string, code string) {
		t.Helper()
		res, body := get(t, q(srv, params...))
		if code == "" {
			if res.StatusCode != 200 {
				t.Errorf("%v = %d %s, want 200", params, res.StatusCode, body)
			}
			return
		}
		if res.StatusCode != 400 {
			t.Errorf("%v = %d %s, want 400 %s", params, res.StatusCode, body, code)
			return
		}
		wantProblem(t, res, body, 400, code)
	}

	t.Run("disabled", func(t *testing.T) {
		srv := testServer(t, func(c *config.Config) { c.Plugins.Disabled = []string{"postgis"} })
		expect(t, srv.URL, spatialFilter, "invalid_filter")
		expect(t, srv.URL, pseudo, "unknown_column")
		expect(t, srv.URL, dist, "invalid_filter")
		expect(t, srv.URL, proj, "invalid_select")
		// Core GeoJSON read/write and is.null keep working.
		var rows []row
		getJSON(t, q(srv.URL, "select=id,location", "area=is.null", "order=id"), &rows)
		if got := ids(rows, "id"); !eq(got, []int{2, 3, 4}) || rows[0]["location"].(map[string]any)["type"] != "Point" {
			t.Errorf("core spatial read = %v", rows)
		}
		sweep(t)
		var pl row
		sendJSON(t, "POST", srv.URL+"/api/places?select=id,location", `{"name": "M3 postgis off", "location": "SRID=4326;POINT(1 2)"}`, 201, &pl)
		sendJSON(t, "DELETE", fmt.Sprintf("%s/api/places/%d", srv.URL, rowID(t, pl)), "", 200, nil)
		if pl["location"].(map[string]any)["type"] != "Point" {
			t.Errorf("core spatial write = %v", pl)
		}
		sch := schemas(openAPI(t, srv.URL))
		if _, ok := sch["GeoJSON"]; ok {
			t.Error("GeoJSON schema present with the plugin disabled")
		}
		props := sch["places"].(map[string]any)["properties"].(map[string]any)
		if _, ok := props["location_srid"]; ok {
			t.Error("pseudo-column present with the plugin disabled")
		}
		if _, ok := props["location"].(map[string]any)["anyOf"]; ok {
			t.Error("location still references GeoJSON")
		}
	})
	t.Run("filters=false", func(t *testing.T) {
		srv := testServer(t, func(c *config.Config) {
			c.Plugins.Settings = map[string]map[string]any{"postgis": {"filters": false}}
		})
		expect(t, srv.URL, spatialFilter, "invalid_filter")
		expect(t, srv.URL, pseudo, "")
		expect(t, srv.URL, dist, "")
		expect(t, srv.URL, proj, "")
	})
	t.Run("derived=false", func(t *testing.T) {
		srv := testServer(t, func(c *config.Config) {
			c.Plugins.Settings = map[string]map[string]any{"postgis": {"derived": false}}
		})
		expect(t, srv.URL, spatialFilter, "")
		expect(t, srv.URL, pseudo, "unknown_column")
		expect(t, srv.URL, dist, "")
		expect(t, srv.URL, proj, "")
		if _, ok := schemas(openAPI(t, srv.URL))["places"].(map[string]any)["properties"].(map[string]any)["location_srid"]; ok {
			t.Error("pseudo-column documented with derived=false")
		}
	})
	t.Run("ordering=false", func(t *testing.T) {
		srv := testServer(t, func(c *config.Config) {
			c.Plugins.Settings = map[string]map[string]any{"postgis": {"ordering": false}}
		})
		expect(t, srv.URL, spatialFilter, "")
		expect(t, srv.URL, pseudo, "")
		expect(t, srv.URL, dist, "invalid_filter")
		expect(t, srv.URL, proj, "")
	})
	t.Run("projection=false", func(t *testing.T) {
		srv := testServer(t, func(c *config.Config) {
			c.Plugins.Settings = map[string]map[string]any{"postgis": {"projection": false}}
		})
		expect(t, srv.URL, spatialFilter, "")
		expect(t, srv.URL, pseudo, "")
		expect(t, srv.URL, dist, "")
		expect(t, srv.URL, proj, "invalid_select")
	})
}

func TestPostGISOmitTags(t *testing.T) {
	dbURL := os.Getenv("PDBR_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(ctx, `COMMENT ON COLUMN places.area IS NULL`)
		conn.Close(ctx)
	})
	if _, err := conn.Exec(ctx, `COMMENT ON COLUMN places.area IS '@omit filter,order'`); err != nil {
		t.Fatal(err)
	}

	// smart-comments off: the postgis plugin reads the tag itself.
	srv := testServer(t, func(c *config.Config) { c.Plugins.Disabled = []string{"smart-comments"} })
	res, body := get(t, q(srv.URL, "area=st_intersects.POINT(-0.128 51.508)"))
	wantProblem(t, res, body, 400, "column_not_filterable")
	res, body = get(t, q(srv.URL, "area=is.null"))
	wantProblem(t, res, body, 400, "column_not_filterable")
	res, body = get(t, q(srv.URL, "order=area.dist.asc", "near="+trafalgar))
	wantProblem(t, res, body, 400, "column_not_orderable")
	var rows []row
	getJSON(t, q(srv.URL, "select=id,area_srid", "location=st_intersects."+trafalgar, "order=location.dist.asc", "near="+trafalgar), &rows)
	if got := ids(rows, "id"); !eq(got, []int{1}) || rows[0]["area_srid"] != float64(4326) {
		t.Errorf("location surface disturbed: %v", rows)
	}
}
