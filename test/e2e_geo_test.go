package e2e

import (
	"context"
	"encoding/json"
	"math"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbcore/confx"

	"github.com/suprbdev/pdbr"
	"github.com/suprbdev/pdbr/internal/config"
)

// The cube / earthdistance / raster suite works in geoSchema and installs
// cube, earthdistance and postgis_raster in the database's default schema
// (earthdistance before 1.2 calls the cube functions unqualified, so both
// must be on the search_path on PostgreSQL 15 and earlier). Extensions it
// created are dropped again; it skips when one is unavailable.
const geoSchema = "pdbr_geo"

// geoDDL: shapes.box carries a GiST index (cube operators, thresholds and
// KNN ordering are admitted under filters.indexed_only), shapes.pt none;
// sites.loc (earth) a GiST index, sites.ll (point) none; tiles.rast the
// GiST expression index on ST_ConvexHull that serves intersects. Tile 1
// covers x, y in [0, 10] with band 1 = 7 and band 2 = 3, tile 2 x in [20,
// 30] with one band = 1, tile 3 is NULL. vault's columns are tagged
// @omit read.
const geoDDL = `
CREATE TABLE pdbr_geo.shapes (id int PRIMARY KEY, name text, box cube, pt cube);
CREATE INDEX shapes_box_idx ON pdbr_geo.shapes USING gist (box);
INSERT INTO pdbr_geo.shapes VALUES
  (1, 'a', '(0,0),(1,1)', '(0,0)'),
  (2, 'b', '(2,2),(3,3)', '(5,5)'),
  (3, 'c', '(10,10)', '(1,1)'),
  (4, 'n', NULL, NULL);
CREATE TABLE pdbr_geo.sites (id int PRIMARY KEY, name text, loc earth, ll point);
CREATE INDEX sites_loc_idx ON pdbr_geo.sites USING gist (loc);
INSERT INTO pdbr_geo.sites
SELECT id, name, CASE WHEN lat IS NULL THEN NULL ELSE ll_to_earth(lat, lon) END, CASE WHEN lat IS NULL THEN NULL ELSE point(lon, lat) END
FROM (VALUES (1, 'London', 51.5074, -0.1278), (2, 'Paris', 48.8566, 2.3522), (3, 'New York', 40.7128, -74.0060),
             (4, 'Brighton', 50.8225, -0.1372), (5, 'Nowhere', NULL, NULL)) AS v(id, name, lat, lon);
CREATE TABLE pdbr_geo.tiles (id int PRIMARY KEY, name text, rast raster, history raster[]);
CREATE INDEX tiles_rast_idx ON pdbr_geo.tiles USING gist (ST_ConvexHull(rast));
INSERT INTO pdbr_geo.tiles VALUES
  (1, 'a', ST_AddBand(ST_AddBand(ST_MakeEmptyRaster(10, 10, 0, 10, 1, -1, 0, 0, 4326), 1, '8BUI', 7, 0), 2, '8BUI', 3, 0), NULL),
  (2, 'b', ST_AddBand(ST_MakeEmptyRaster(10, 10, 20, 10, 1, -1, 0, 0, 4326), 1, '8BUI', 1, 0), NULL),
  (3, 'c', NULL, NULL);
UPDATE pdbr_geo.tiles SET history = ARRAY[rast] WHERE id = 1;
CREATE FUNCTION pdbr_geo.tiles_wkb(t pdbr_geo.tiles) RETURNS text LANGUAGE sql STABLE AS $$ SELECT encode(ST_AsBinary(t.rast), 'base64') $$;
CREATE TABLE pdbr_geo.vault (id int PRIMARY KEY, rast raster, loc earth, box cube);
COMMENT ON COLUMN pdbr_geo.vault.rast IS '@omit read';
COMMENT ON COLUMN pdbr_geo.vault.loc IS '@omit read';
COMMENT ON COLUMN pdbr_geo.vault.box IS '@omit read';
CREATE INDEX vault_loc_idx ON pdbr_geo.vault USING gist (loc);
CREATE INDEX vault_box_idx ON pdbr_geo.vault USING gist (box);
INSERT INTO pdbr_geo.vault SELECT 1, rast, ll_to_earth(0, 0), '(1,2)' FROM pdbr_geo.tiles WHERE id = 1;
`

// geoSetup installs the extensions and geoDDL and returns the database URL.
func geoSetup(t *testing.T) string {
	t.Helper()
	dbURL := os.Getenv("PDBR_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var created []string
	t.Cleanup(func() {
		c, err := pgx.Connect(ctx, dbURL)
		if err != nil {
			t.Errorf("cleanup: %v", err)
			return
		}
		defer c.Close(ctx)
		_, _ = c.Exec(ctx, `DROP SCHEMA IF EXISTS `+geoSchema+` CASCADE`)
		for i := len(created) - 1; i >= 0; i-- {
			_, _ = c.Exec(ctx, `DROP EXTENSION IF EXISTS `+created[i]+` CASCADE`)
		}
	})
	for _, ext := range []string{"cube", "earthdistance", "postgis_raster"} {
		var n int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_extension WHERE extname = $1`, ext).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			continue
		}
		if _, err := conn.Exec(ctx, `CREATE EXTENSION `+ext); err != nil {
			t.Skipf("extension %s unavailable: %v", ext, err)
		}
		created = append(created, ext)
	}
	if _, err := conn.Exec(ctx, `DROP SCHEMA IF EXISTS `+geoSchema+` CASCADE; CREATE SCHEMA `+geoSchema+`;`+geoDDL); err != nil {
		t.Fatal(err)
	}
	return dbURL
}

func geoConfig(dbURL string, mutate func(*config.Config)) config.Config {
	cfg := config.DefaultConfig()
	cfg.Database.URL = dbURL
	cfg.UseConnectionIdentity()
	cfg.Errors.Detail = "dev"
	cfg.Schema.Schemas = []string{geoSchema}
	if mutate != nil {
		mutate(&cfg)
	}
	return cfg
}

// geoServer boots pdbr over geoSchema; geoSetup must have run.
func geoServer(t *testing.T, dbURL string, mutate func(*config.Config)) string {
	t.Helper()
	cfg := geoConfig(dbURL, mutate)
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	app := pdbr.New(cfg, pdbr.WithVersion("e2e"))
	t.Cleanup(app.Close)
	h, err := app.Handler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

// explainGeo compiles target with the server's configuration and returns
// the plan PostgreSQL chooses for it with sequential scans disabled.
func explainGeo(t *testing.T, dbURL, target string, mutate func(*config.Config)) string {
	t.Helper()
	cfg := geoConfig(dbURL, mutate)
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	app := pdbr.New(cfg, pdbr.WithVersion("e2e"))
	defer app.Close()
	out, err := app.CompileSQL(context.Background(), httptest.NewRequest("GET", target, nil))
	if err != nil {
		t.Fatalf("compile %s: %v", target, err)
	}
	_, rest, _ := strings.Cut(out, "\n")
	sqlText, argText, ok := strings.Cut(rest, "\n-- args: ")
	if !ok {
		t.Fatalf("no args in %s", out)
	}
	var args []any
	if err := json.Unmarshal([]byte(argText), &args); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, "EXPLAIN (COSTS OFF) "+sqlText, args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var sb strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		sb.WriteString(line + "\n")
	}
	return sb.String()
}

func nextLink(link string) string {
	for _, part := range strings.Split(link, ", ") {
		if u, ok := strings.CutSuffix(part, `>; rel="next"`); ok {
			return strings.TrimPrefix(u, "<")
		}
	}
	return ""
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}

// TestCube: cube values read and write as JSON lists, cs/cd/ov and the
// distance thresholds filter, the three distances order nearest first
// against vec= and come out as scores, and generic comparisons and plain
// ordering are refused.
func TestCube(t *testing.T) {
	dbURL := geoSetup(t)
	srv := geoServer(t, dbURL, nil)
	var rows []row
	getJSON(t, srv+"/api/shapes?select=id,box,pt&order=id", &rows)
	if !sameJSONValue(rows[0]["box"], `[[0,0],[1,1]]`) || !sameJSONValue(rows[0]["pt"], `[0,0]`) || !sameJSONValue(rows[2]["box"], `[10,10]`) || rows[3]["box"] != nil {
		t.Errorf("cube output = %v", rows)
	}
	v := url.QueryEscape("[0,0]")
	wantIDs(t, srv, "shapes?box=cs."+url.QueryEscape("[0.5,0.5]"), []int{1})
	wantIDs(t, srv, "shapes?box=cd.(0,0),(5,5)&order=id", []int{1, 2})
	wantIDs(t, srv, "shapes?box=ov."+url.QueryEscape("[[0.5,0.5],[2.5,2.5]]")+"&order=id", []int{1, 2})
	wantIDs(t, srv, "shapes?or=(box.cs.\"(10,10)\",box.is.null)&order=id", []int{3, 4})
	wantIDs(t, srv, "shapes?box=vec_l2_lt.3&vec="+v+"&order=id", []int{1, 2})
	wantIDs(t, srv, "shapes?box=not.vec_chebyshev_lt.5&vec="+v+"&order=id", []int{3})
	wantIDs(t, srv, "shapes?box=vec_l1_lt.1&vec=(0,0)", []int{1})
	for order, want := range map[string][]int{
		"box.vec_l2": {1, 2, 3, 4}, "box.vec_l1": {1, 2, 3, 4}, "box.vec_chebyshev": {1, 2, 3, 4}, "box.vec_l2.desc": {3, 2, 1, 4},
	} {
		wantIDs(t, srv, "shapes?select=id&order="+order+"&vec="+v, want)
	}
	getJSON(t, srv+"/api/shapes?select=id,box_l2_distance,box_l1_distance,box_chebyshev_distance,pt_l2_distance&id=eq.2&vec="+v, &rows)
	r := rows[0]
	if math.Abs(num(r["box_l2_distance"])-math.Sqrt(8)) > 1e-9 || num(r["box_l1_distance"]) != 4 || num(r["box_chebyshev_distance"]) != 2 || math.Abs(num(r["pt_l2_distance"])-math.Sqrt(50)) > 1e-9 {
		t.Errorf("cube scores = %v", r)
	}

	var created row
	sendJSON(t, "POST", srv+"/api/shapes?select=id,box,pt", `{"id": 10, "name": "w", "box": [[1, 2], [3, 4]], "pt": "(7, 8)"}`, 201, &created)
	if !sameJSONValue(created["box"], `[[1,2],[3,4]]`) || !sameJSONValue(created["pt"], `[7,8]`) {
		t.Errorf("created = %v", created)
	}
	created = nil
	sendJSON(t, "PATCH", srv+"/api/shapes/10?select=pt", `{"pt": [9]}`, 200, &created)
	if !sameJSONValue(created["pt"], `[9]`) {
		t.Errorf("patched = %v", created)
	}
	for body, code := range map[string]string{
		`{"id": 11, "box": [[1], [1, 2]]}`: "invalid_body",
		`{"id": 11, "box": [1, "a"]}`:      "invalid_body",
		`{"id": 11, "box": "garbage"}`:     "pg:22P02",
	} {
		res, out := send(t, "POST", srv+"/api/shapes", body, nil)
		var p problem
		_ = json.Unmarshal(out, &p)
		if res.StatusCode != 400 || p.Code != code {
			t.Errorf("POST %s = %d %s, want 400 %s", body, res.StatusCode, out, code)
		}
	}

	wantCode(t, srv, "shapes?box=eq.(0,0)", 400, "invalid_filter")
	wantCode(t, srv, "shapes?order=box", 400, "column_not_orderable")
	wantCode(t, srv, "shapes?distinct=box", 400, "invalid_filter")
	wantCode(t, srv, "shapes?order=box.vec_l2", 400, "invalid_filter")
	wantCode(t, srv, "shapes?order=pt.vec_l2&vec="+v, 400, "column_not_orderable")
	wantCode(t, srv, "shapes?pt=vec_l2_lt.1&vec="+v, 400, "column_not_filterable")
	wantCode(t, srv, "shapes?order=box.vec_l2&vec="+url.QueryEscape("[[0],[1,2]]"), 400, "invalid_filter")

	open := geoServer(t, dbURL, func(c *config.Config) { c.Filters.IndexedOnly = false })
	wantIDs(t, open, "shapes?select=id&pt=vec_l2_lt.2&order=pt.vec_l2&vec="+v, []int{1, 3})
}

// TestEarth: earth columns read and write as {lat, lon}, earth_lt is a
// radius in metres around latlon=, earth_dist orders nearest first (keyset
// cursors and Link headers walk it) and the score is the great-circle
// distance; point columns get <@> miles only where the policy admits it.
func TestEarth(t *testing.T) {
	dbURL := geoSetup(t)
	srv := geoServer(t, dbURL, nil)
	var rows []row
	getJSON(t, srv+"/api/sites?select=id,loc&id=in.(1,5)&order=id", &rows)
	loc, _ := rows[0]["loc"].(map[string]any)
	if math.Abs(num(loc["lat"])-51.5074) > 1e-6 || math.Abs(num(loc["lon"])+0.1278) > 1e-6 || rows[1]["loc"] != nil {
		t.Errorf("earth output = %v", rows)
	}
	london := "latlon=51.5074,-0.1278"
	wantIDs(t, srv, "sites?loc=earth_lt.100000&order=id&"+london, []int{1, 4})
	wantIDs(t, srv, "sites?loc=not.earth_lt.400000&order=id&"+london, []int{3})
	wantIDs(t, srv, "sites?select=id&order=loc.earth_dist&"+london, []int{1, 4, 2, 3, 5})
	wantIDs(t, srv, "sites?select=id&order=loc.earth_dist.desc&loc=not.is.null&"+london, []int{3, 2, 4, 1})
	getJSON(t, srv+"/api/sites?select=id,loc_earth_distance&id=eq.2&"+london, &rows)
	if d := num(rows[0]["loc_earth_distance"]); d < 340000 || d > 347000 {
		t.Errorf("London-Paris = %v m", rows[0]["loc_earth_distance"])
	}

	var walked []int
	next := "/api/sites?select=id&order=loc.earth_dist&limit=2&" + london
	for range 5 {
		res := getJSON(t, srv+next, &rows)
		walked = append(walked, ids(rows, "id")...)
		if next = nextLink(res.Header.Get("Link")); next == "" {
			break
		}
		if !strings.Contains(next, "after=") {
			t.Fatalf("next link without a cursor: %s", next)
		}
	}
	if !eq(walked, []int{1, 4, 2, 3, 5}) {
		t.Errorf("keyset walk by earth distance = %v", walked)
	}

	var created row
	sendJSON(t, "POST", srv+"/api/sites?select=id,loc,loc_earth_distance&"+london, `{"id": 9, "name": "Trafalgar", "loc": {"lat": 51.508, "lon": -0.128}, "ll": "(-0.128,51.508)"}`, 201, &created)
	loc, _ = created["loc"].(map[string]any)
	if math.Abs(num(loc["lat"])-51.508) > 1e-6 || num(created["loc_earth_distance"]) > 100 {
		t.Errorf("created = %v", created)
	}
	for body, code := range map[string]string{
		`{"id": 8, "loc": {"lat": 91, "lon": 0}}`: "invalid_body",
		`{"id": 8, "loc": {"lat": 1}}`:            "invalid_body",
		`{"id": 8, "loc": [1, 2]}`:                "invalid_body",
	} {
		res, out := send(t, "POST", srv+"/api/sites", body, nil)
		var p problem
		_ = json.Unmarshal(out, &p)
		if res.StatusCode != 400 || p.Code != code {
			t.Errorf("POST %s = %d %s, want 400 %s", body, res.StatusCode, out, code)
		}
	}

	wantCode(t, srv, "sites?order=loc.earth_dist", 400, "invalid_filter")
	wantCode(t, srv, "sites?order=loc.earth_dist&latlon=95,0", 400, "invalid_filter")
	wantCode(t, srv, "sites?"+london, 400, "invalid_filter")
	wantCode(t, srv, "sites?order=loc", 400, "column_not_orderable")
	wantCode(t, srv, "sites?loc=eq.x", 400, "invalid_filter")
	wantCode(t, srv, "sites?order=ll.earth_miles&"+london, 400, "column_not_orderable")
	wantCode(t, srv, "sites?select=ll_earth_distance_miles&"+london, 400, "unknown_column")

	allowed := geoServer(t, dbURL, func(c *config.Config) {
		c.Filters.AllowColumns = map[string][]string{geoSchema + ".sites": {"ll"}}
	})
	wantIDs(t, allowed, "sites?select=id&ll=earth_miles_lt.100&order=ll.earth_miles&id=neq.9&"+london, []int{1, 4})
	getJSON(t, allowed+"/api/sites?select=id,ll_earth_distance_miles&id=eq.4&"+london, &rows)
	if d := num(rows[0]["ll_earth_distance_miles"]); d < 45 || d > 50 {
		t.Errorf("London-Brighton = %v miles", rows[0]["ll_earth_distance_miles"])
	}
}

// TestGeoKNNUsesIndex: nearest-first ordering by an earth or cube distance
// is answered by the column's GiST index (a KNN index scan under an
// incremental sort for the primary-key tiebreaker, which PostgreSQL plans
// from version 17 on), as are the earth radius filter, the cube threshold
// prefilter and the raster intersects filter.
func TestGeoKNNUsesIndex(t *testing.T) {
	dbURL := geoSetup(t)
	knn := dbText(t, `SELECT (current_setting('server_version_num')::int >= 170000)::text`) == "true"
	for target, index := range map[string]string{
		"/api/sites?select=id&order=loc.earth_dist&limit=2&latlon=51.5,0":                "sites_loc_idx",
		"/api/sites?select=id&loc=earth_lt.10000&latlon=51.5,0":                          "sites_loc_idx",
		"/api/shapes?select=id&order=box.vec_l2&limit=2&vec=" + url.QueryEscape("[0,0]"): "shapes_box_idx",
		"/api/shapes?select=id&order=box.vec_chebyshev&limit=2&vec=(0,0)":                "shapes_box_idx",
		"/api/shapes?select=id&box=vec_l1_lt.2&vec=(0,0)":                                "shapes_box_idx",
		"/api/tiles?select=id&rast=intersects.SRID%3D4326%3BPOINT(5%205)":                "tiles_rast_idx",
	} {
		if strings.Contains(target, "order=") && !knn {
			continue
		}
		plan := explainGeo(t, dbURL, target, func(c *config.Config) {
			c.Filters.AllowColumns = map[string][]string{geoSchema + ".tiles": {"rast"}}
		})
		if !strings.Contains(plan, index) {
			t.Errorf("%s does not use %s:\n%s", target, index, plan)
		}
		if strings.Contains(target, "order=") && !strings.Contains(plan, "Order By:") {
			t.Errorf("%s is not a KNN scan:\n%s", target, plan)
		}
	}
}

// TestRaster: a raster column is never returned (not in select=*, 400 on
// an explicit select), stays writable, filters with intersects and
// is.null, and is read through the metadata, pixel value and statistics
// pseudo-columns; a computed column is the opt-in for its bytes.
func TestRaster(t *testing.T) {
	dbURL := geoSetup(t)
	allow := func(c *config.Config) {
		c.Filters.AllowColumns = map[string][]string{geoSchema + ".tiles": {"rast"}}
	}
	srv := geoServer(t, dbURL, allow)
	res, body := get(t, srv+"/api/tiles?order=id")
	if res.StatusCode != 200 || strings.Contains(string(body), "rast") || strings.Contains(string(body), "history") || !strings.Contains(string(body), `"name"`) {
		t.Errorf("select=* = %d %s", res.StatusCode, body)
	}
	res, body = get(t, srv+"/api/tiles/1")
	if res.StatusCode != 200 || strings.Contains(string(body), "rast") || res.Header.Get("ETag") == "" {
		t.Errorf("row = %d %s %v", res.StatusCode, body, res.Header)
	}
	etag := res.Header.Get("ETag")

	var rows []row
	getJSON(t, srv+"/api/tiles?select=id,rast_width,rast_height,rast_num_bands,rast_srid,rast_pixel_width,rast_pixel_height,rast_envelope&order=id", &rows)
	if r := rows[0]; num(r["rast_width"]) != 10 || num(r["rast_height"]) != 10 || num(r["rast_num_bands"]) != 2 || num(r["rast_srid"]) != 4326 || num(r["rast_pixel_width"]) != 1 || num(r["rast_pixel_height"]) != 1 {
		t.Errorf("tile 1 accessors = %v", r)
	}
	if env, _ := rows[0]["rast_envelope"].(map[string]any); env["type"] != "Polygon" || rows[2]["rast_envelope"] != nil {
		t.Errorf("envelopes = %v / %v", rows[0]["rast_envelope"], rows[2]["rast_envelope"])
	}
	getJSON(t, srv+"/api/tiles?select=id,e:rast_envelope(precision=0)&id=eq.1", &rows)
	if env, _ := rows[0]["e"].(map[string]any); env["type"] != "Polygon" {
		t.Errorf("envelope with precision = %v", rows[0]["e"])
	}
	getJSON(t, srv+"/api/tiles?select=id,rast_value,b2:rast_value(band=2),rast_summary_stats,s2:rast_summary_stats(band=2)&order=id&near=POINT(5%205)", &rows)
	if num(rows[0]["rast_value"]) != 7 || num(rows[0]["b2"]) != 3 || rows[1]["rast_value"] != nil || rows[1]["b2"] != nil || rows[2]["rast_value"] != nil {
		t.Errorf("pixel values = %v", rows)
	}
	if st, _ := rows[0]["rast_summary_stats"].(map[string]any); num(st["count"]) != 100 || num(st["sum"]) != 700 || num(st["mean"]) != 7 || num(st["min"]) != 7 || num(st["max"]) != 7 {
		t.Errorf("summary stats = %v", rows[0]["rast_summary_stats"])
	}
	if st, _ := rows[0]["s2"].(map[string]any); num(st["mean"]) != 3 || rows[1]["s2"] != nil {
		t.Errorf("band 2 stats = %v / %v", rows[0]["s2"], rows[1]["s2"])
	}
	near := url.QueryEscape(`{"type":"Point","coordinates":[25,5]}`)
	getJSON(t, srv+"/api/tiles?select=id,rast_value&id=eq.2&near="+near, &rows)
	if num(rows[0]["rast_value"]) != 1 {
		t.Errorf("GeoJSON near value = %v", rows)
	}

	wantIDs(t, srv, "tiles?select=id&rast=intersects.SRID%3D4326%3BPOINT(5%205)", []int{1})
	wantIDs(t, srv, "tiles?select=id&rast=not.intersects.SRID%3D4326%3BPOINT(5%205)&order=id", []int{2, 3})
	poly := url.QueryEscape(`{"type":"Polygon","coordinates":[[[-1,-1],[31,-1],[31,11],[-1,11],[-1,-1]]]}`)
	wantIDs(t, srv, "tiles?select=id&order=id&rast=intersects."+poly, []int{1, 2})
	wantIDs(t, srv, "tiles?select=id&or=(rast.is.null,rast.intersects.\"SRID%3D4326%3BPOINT(25%205)\")&order=id", []int{2, 3})
	wantCode(t, srv, "tiles?select=id&rast=intersects.SRID%3D3857%3BPOINT(5%205)", 400, "pg:P0001")

	var wkb []row
	getJSON(t, srv+"/api/tiles?select=wkb&id=eq.1", &wkb)
	if s, _ := wkb[0]["wkb"].(string); s == "" {
		t.Errorf("computed raster bytes = %v", wkb)
	}
	hex := dbText(t, `SELECT encode(ST_AsBinary(rast), 'hex') FROM `+geoSchema+`.tiles WHERE id = 2`)
	var created row
	sendJSON(t, "POST", srv+"/api/tiles?select=id,rast_width,rast_num_bands", `{"id": 10, "name": "copy", "rast": "`+hex+`"}`, 201, &created)
	if num(created["rast_width"]) != 10 || num(created["rast_num_bands"]) != 1 {
		t.Errorf("written raster = %v", created)
	}
	res, body = send(t, "PATCH", srv+"/api/tiles/1", `{"rast": "`+hex+`"}`, map[string]string{"Prefer": "return=representation"})
	if res.StatusCode != 200 || strings.Contains(string(body), `"rast"`) {
		t.Errorf("PATCH = %d %s", res.StatusCode, body)
	}
	res, _ = get(t, srv+"/api/tiles/1")
	if res.Header.Get("ETag") != etag {
		t.Errorf("ETag changed with the raster: %s -> %s", etag, res.Header.Get("ETag"))
	}

	wantCode(t, srv, "tiles?select=id,rast", 400, "invalid_select")
	wantCode(t, srv, "tiles?select=history", 400, "invalid_select")
	wantCode(t, srv, "tiles?order=rast", 400, "column_not_orderable")
	wantCode(t, srv, "tiles?rast=eq.x", 400, "invalid_filter")
	wantCode(t, srv, "tiles?select=rast_value", 400, "invalid_filter")
	wantCode(t, srv, "tiles?select=rast_value(band=0)&near=POINT(1%201)", 400, "invalid_select")

	strict := geoServer(t, dbURL, func(c *config.Config) { c.Server.MaxCost = 5000 })
	wantCode(t, strict, "tiles?select=rast_summary_stats", 400, "cost_exceeded")
	wantIDs(t, strict, "tiles?select=id,rast_summary_stats&limit=10&order=id", []int{1, 2, 3, 10})
	wantCode(t, strict, "tiles?rast=intersects.POINT(1%201)", 400, "column_not_filterable")
}

// TestGeoOmitAndContracts: @omit read on raster, earth and cube columns
// and a contract hiding them remove every piece of their surface (values,
// pseudo-columns, scores, filters, order terms, OpenAPI) while @omit read
// keeps them writable.
func TestGeoOmitAndContracts(t *testing.T) {
	dbURL := geoSetup(t)
	srv := geoServer(t, dbURL, nil)
	for _, q := range []string{
		"vault?select=rast_width", "vault?select=loc", "vault?select=loc_earth_distance&latlon=0,0",
		"vault?select=box_l2_distance&vec=(0,0)", "vault?rast=is.null", "vault?order=loc.earth_dist&latlon=0,0",
	} {
		if res, body := get(t, srv+"/api/"+q); res.StatusCode != 400 {
			t.Errorf("%s = %d %s", q, res.StatusCode, body)
		}
	}
	_, doc := fetchOpenAPI(t, srv)
	for _, k := range []string{"rast", "loc", "box", "rast_width", "loc_earth_distance", "box_l2_distance"} {
		if componentSchema(doc, "vault")["properties"].(map[string]any)[k] != nil {
			t.Errorf("vault schema documents %s", k)
		}
	}
	sendJSON(t, "POST", srv+"/api/vault", `{"id": 2, "loc": {"lat": 1, "lon": 2}, "box": [1, 2]}`, 201, nil)

	execDDL(t, dbURL,
		`COMMENT ON COLUMN `+geoSchema+`.sites.loc IS '@behavior -contract:public'`,
		`COMMENT ON COLUMN `+geoSchema+`.tiles.rast IS '@behavior -contract:public'`)
	contracts := geoServer(t, dbURL, func(c *config.Config) {
		c.Contracts = []confx.Contract{{Name: "internal"}, {Name: "public"}}
	})
	for _, q := range []string{"tiles?select=rast_width", "sites?select=loc_earth_distance&latlon=0,0", "sites?order=loc.earth_dist&latlon=0,0"} {
		if res, body := get(t, contracts+"/api/public/"+q); res.StatusCode != 400 {
			t.Errorf("public %s = %d %s", q, res.StatusCode, body)
		}
		if res, body := get(t, contracts+"/api/internal/"+q); res.StatusCode != 200 {
			t.Errorf("internal %s = %d %s", q, res.StatusCode, body)
		}
	}
}

// TestGeoOpenAPI: the document validates against the 3.1 meta-schema and
// documents the cube, earth and raster surface.
func TestGeoOpenAPI(t *testing.T) {
	dbURL := geoSetup(t)
	srv := geoServer(t, dbURL, func(c *config.Config) {
		c.Filters.AllowColumns = map[string][]string{geoSchema + ".tiles": {"rast"}}
	})
	body, doc := fetchOpenAPI(t, srv)
	validateOpenAPI(t, oasSchema(t), body)
	sites := componentSchema(doc, "sites")["properties"].(map[string]any)
	if loc, _ := sites["loc"].(map[string]any); loc == nil || loc["properties"] == nil {
		t.Errorf("sites.loc schema = %v", sites["loc"])
	}
	if sites["loc_earth_distance"] == nil {
		t.Error("sites lacks loc_earth_distance")
	}
	tiles := componentSchema(doc, "tiles")["properties"].(map[string]any)
	if tiles["rast"] != nil || tiles["history"] != nil || tiles["rast_summary_stats"] == nil || tiles["rast_value"] == nil {
		t.Errorf("tiles schema = %v", keysOf(tiles))
	}
	if componentSchema(doc, "tiles.insert")["properties"].(map[string]any)["rast"] == nil {
		t.Error("tiles.insert lacks rast")
	}
	params := opParams(pathOp(doc, "/sites", "get"))
	if params["query:latlon"] == nil {
		t.Errorf("sites params = %v", keysOfParams(params))
	}
	tp := opParams(pathOp(doc, "/tiles", "get"))
	if tp["query:near"] == nil || fmtAny(tp["query:rast"]["x-pdbr-filter-ops"]) != fmtAny([]any{"intersects", "is"}) {
		t.Errorf("tiles params = %v / %v", keysOfParams(tp), tp["query:rast"])
	}
	sp := opParams(pathOp(doc, "/shapes", "get"))
	if sp["query:vec"] == nil || !strings.Contains(fmtAny(sp["query:box"]["x-pdbr-filter-ops"]), "vec_chebyshev_lt") {
		t.Errorf("shapes params = %v / %v", keysOfParams(sp), sp["query:box"])
	}
}
