package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbr/internal/config"
)

const geoJSON = "application/geo+json"

type geoFeature struct {
	Type       string         `json:"type"`
	Geometry   map[string]any `json:"geometry"`
	Properties map[string]any `json:"properties"`
}

type geoCollection struct {
	Type     string       `json:"type"`
	Features []geoFeature `json:"features"`
}

var geoFixturesOnce sync.Once

func geoFixtures(t *testing.T) {
	t.Helper()
	url := os.Getenv("PDBR_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBR_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	geoFixturesOnce.Do(func() {
		ctx := context.Background()
		conn, err := pgx.Connect(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(ctx)
		for _, ddl := range []string{
			`CREATE OR REPLACE FUNCTION public.geo_places_of(oid integer) RETURNS SETOF places STABLE LANGUAGE sql AS $$
			   SELECT * FROM places WHERE owner_id = oid ORDER BY id $$`,
			`CREATE OR REPLACE FUNCTION public.geo_first_place() RETURNS places STABLE LANGUAGE sql AS $$
			   SELECT * FROM places ORDER BY id LIMIT 1 $$`,
		} {
			if _, err := conn.Exec(ctx, ddl); err != nil {
				t.Fatalf("geojson fixture: %v", err)
			}
		}
	})
}

func geoGet(t *testing.T, url string, want int, extra map[string]string) (*geoCollection, http.Header) {
	t.Helper()
	hdr := map[string]string{"Accept": geoJSON}
	for k, v := range extra {
		hdr[k] = v
	}
	res, body := do(t, "GET", url, hdr)
	if res.StatusCode != want {
		t.Fatalf("GET %s = %d %s, want %d", url, res.StatusCode, body, want)
	}
	if want >= 400 {
		return nil, res.Header
	}
	if ct := res.Header.Get("Content-Type"); ct != geoJSON {
		t.Fatalf("GET %s Content-Type = %q", url, ct)
	}
	var fc geoCollection
	if err := json.Unmarshal(body, &fc); err != nil || fc.Type != "FeatureCollection" {
		t.Fatalf("GET %s: not a FeatureCollection: %s (%v)", url, body, err)
	}
	return &fc, res.Header
}

func geoProblem(t *testing.T, url string, hdr map[string]string, status int, code string) {
	t.Helper()
	res, body := do(t, "GET", url, hdr)
	var p struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(body, &p)
	if res.StatusCode != status || p.Code != code {
		t.Errorf("GET %s = %d %s, want %d %s", url, res.StatusCode, body, status, code)
	}
}

func TestGeoJSONList(t *testing.T) {
	srv := testServer(t, nil).URL
	fc, h := geoGet(t, srv+"/api/places?select=id,name,location&order=id", 200, nil)
	if len(fc.Features) != 4 {
		t.Fatalf("features = %+v", fc.Features)
	}
	f := fc.Features[0]
	coords, _ := f.Geometry["coordinates"].([]any)
	if f.Type != "Feature" || f.Geometry["type"] != "Point" || len(coords) != 2 || coords[0] != -0.1281 || coords[1] != 51.508 {
		t.Errorf("feature 0 = %+v", f)
	}
	if len(f.Properties) != 2 || f.Properties["id"] != float64(1) || f.Properties["name"] != "Trafalgar Square" {
		t.Errorf("properties = %v", f.Properties)
	}
	if got := h.Get("Content-Range"); got != "0-3/*" {
		t.Errorf("Content-Range = %q", got)
	}

	fc, _ = geoGet(t, srv+"/api/places?order=id", 200, nil)
	if len(fc.Features) != 4 || fc.Features[0].Geometry["type"] != "Point" {
		t.Fatalf("star: %+v", fc.Features)
	}
	props := fc.Features[0].Properties
	if _, hasLoc := props["location"]; hasLoc || props["name"] != "Trafalgar Square" || props["owner_id"] != float64(1) {
		t.Errorf("star properties = %v", props)
	}
	if area, _ := props["area"].(map[string]any); area["type"] != "Polygon" {
		t.Errorf("other spatial column should stay a GeoJSON property: %v", props["area"])
	}
	if props2 := fc.Features[1].Properties; props2["area"] != nil {
		t.Errorf("null area property = %v", props2["area"])
	}

	fc, _ = geoGet(t, srv+"/api/places?select=id,location,area&~geometry=area&order=id", 200, nil)
	if fc.Features[0].Geometry["type"] != "Polygon" || fc.Features[1].Geometry != nil {
		t.Errorf("~geometry=area: %+v", fc.Features)
	}
	if loc, _ := fc.Features[0].Properties["location"].(map[string]any); loc["type"] != "Point" || len(fc.Features[0].Properties) != 2 {
		t.Errorf("~geometry=area properties = %v", fc.Features[0].Properties)
	}

	fc, _ = geoGet(t, srv+"/api/places?select=id,pos:location(transform=3857)&id=eq.1", 200, nil)
	if c, _ := fc.Features[0].Geometry["coordinates"].([]any); len(c) != 2 || c[0].(float64) > -14000 {
		t.Errorf("transform=3857 geometry = %v", fc.Features[0].Geometry)
	}
	if _, ok := fc.Features[0].Properties["pos"]; ok {
		t.Errorf("the geometry item must not repeat in properties: %v", fc.Features[0].Properties)
	}
}

func TestGeoJSONQueryAndPaging(t *testing.T) {
	srv := testServer(t, nil).URL
	fc, _ := geoGet(t, srv+"/api/places?select=id,location&location=bbox_intersects.(-0.5,51.3,0.3,51.7)&order=id.desc", 200, nil)
	if len(fc.Features) != 3 || fc.Features[0].Properties["id"] != float64(3) {
		t.Errorf("filter+order: %+v", fc.Features)
	}

	fc, h := geoGet(t, srv+"/api/places?select=id,location&order=id&limit=2&count=true", 206, nil)
	if len(fc.Features) != 2 {
		t.Errorf("features = %+v", fc.Features)
	}
	if h.Get("X-Total-Count") != "4" || h.Get("Content-Range") != "0-1/4" || h.Get("X-Next-Cursor") == "" {
		t.Errorf("page headers = %v", h)
	}
	next := h.Get("X-Next-Cursor")
	fc, _ = geoGet(t, srv+"/api/places?select=id,location&order=id&limit=2&after="+next, 200, nil)
	if len(fc.Features) != 2 || fc.Features[0].Properties["id"] != float64(3) {
		t.Errorf("after cursor: %+v", fc.Features)
	}

	fc, h = geoGet(t, srv+"/api/places?select=id,location&order=id", 206, map[string]string{"Range": "1-2", "Prefer": "count=exact", "Range-Unit": "items"})
	if len(fc.Features) != 2 || fc.Features[0].Properties["id"] != float64(2) || h.Get("Content-Range") != "1-2/4" {
		t.Errorf("Range: %+v %v", fc.Features, h)
	}
	geoGet(t, srv+"/api/places?select=id,location&order=id&offset=10", 416, map[string]string{"Prefer": "count=exact"})

	fc, _ = geoGet(t, srv+"/api/places?select=id,location&id=eq.999", 200, nil)
	if fc.Features == nil || len(fc.Features) != 0 {
		t.Errorf("empty result must be an empty FeatureCollection: %+v", fc)
	}

	res, body := do(t, "HEAD", srv+"/api/places?select=id,location", map[string]string{"Accept": geoJSON})
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != geoJSON || len(body) != 0 {
		t.Errorf("HEAD = %d %q %q", res.StatusCode, res.Header.Get("Content-Type"), body)
	}

	fc, _ = geoGet(t, srv+"/api/places?select=id,location,users(email)&order=id&limit=1", 200, nil)
	if u, _ := fc.Features[0].Properties["users"].(map[string]any); u["email"] != "ada@example.com" {
		t.Errorf("embed property = %v", fc.Features[0].Properties)
	}
}

func TestGeoJSONRow(t *testing.T) {
	srv := testServer(t, nil).URL
	res, body := do(t, "GET", srv+"/api/places/1?select=id,name,location", map[string]string{"Accept": geoJSON})
	var f geoFeature
	if err := json.Unmarshal(body, &f); err != nil || res.StatusCode != 200 || f.Type != "Feature" || f.Geometry["type"] != "Point" || f.Properties["name"] != "Trafalgar Square" {
		t.Fatalf("row = %d %s", res.StatusCode, body)
	}
	if res.Header.Get("Content-Type") != geoJSON || res.Header.Get("ETag") == "" {
		t.Errorf("headers = %v", res.Header)
	}
	geoProblem(t, srv+"/api/places/999", map[string]string{"Accept": geoJSON}, 404, "not_found")
}

func TestGeoJSONRPC(t *testing.T) {
	geoFixtures(t)
	srv := testServer(t, nil).URL
	fc, h := geoGet(t, srv+"/api/rpc/geo_places_of?oid=1&select=id,location", 200, nil)
	if len(fc.Features) != 2 || fc.Features[1].Properties["id"] != float64(2) || h.Get("Content-Range") != "0-1/*" {
		t.Errorf("setof: %+v %v", fc.Features, h)
	}
	res, body := do(t, "GET", srv+"/api/rpc/geo_first_place?select=id,location", map[string]string{"Accept": geoJSON})
	var f geoFeature
	if err := json.Unmarshal(body, &f); err != nil || res.StatusCode != 200 || f.Type != "Feature" || f.Properties["id"] != float64(1) || f.Geometry["type"] != "Point" {
		t.Errorf("row rpc = %d %s", res.StatusCode, body)
	}
	geoProblem(t, srv+"/api/rpc/best_mood", map[string]string{"Accept": geoJSON}, 406, "not_acceptable")
}

func TestGeoJSONRejections(t *testing.T) {
	srv := testServer(t, nil).URL
	acc := map[string]string{"Accept": geoJSON}
	geoProblem(t, srv+"/api/users", acc, 406, "not_acceptable")
	geoProblem(t, srv+"/api/places?select=id,name", acc, 406, "not_acceptable")
	agg := testServer(t, func(c *config.Config) { c.Aggregates.Enabled = true }).URL
	geoProblem(t, agg+"/api/places?select=count()", acc, 406, "not_acceptable")
	geoProblem(t, srv+"/api/places?select=id,location&~geometry=name", acc, 400, "invalid_select")
	geoProblem(t, srv+"/api/places?select=id,location&~geometry=nope", acc, 400, "invalid_select")
	geoProblem(t, srv+"/api/places?~geometry=location", nil, 400, "invalid_parameter")

	res, body := send(t, "POST", srv+"/api/places", `{"name":"geo-reject"}`, map[string]string{"Accept": geoJSON, "Content-Type": "application/json"})
	if res.StatusCode != 406 {
		t.Errorf("POST = %d %s", res.StatusCode, body)
	}

	res, body = do(t, "GET", srv+"/api/places?select=id,location&order=id", map[string]string{"Accept": geoJSON + "; q=0.5, application/json"})
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/json; charset=utf-8" || body[0] != '[' {
		t.Errorf("q-value preference = %d %q %s", res.StatusCode, res.Header.Get("Content-Type"), body)
	}
}

func TestGeoJSONOpenAPI(t *testing.T) {
	srv := testServer(t, nil).URL
	var doc map[string]any
	getJSON(t, srv+"/api/openapi.json", &doc)
	list := pathOp(doc, "/places", "get")
	content, _ := list["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)
	if content[geoJSON] == nil {
		t.Errorf("places list content = %v", content)
	}
	if users := pathOp(doc, "/users", "get"); users["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)[geoJSON] != nil {
		t.Error("users must not advertise geo+json")
	}
	row := pathOp(doc, "/places/{id}", "get")
	if c, _ := row["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any); c[geoJSON] == nil {
		t.Errorf("places row content = %v", c)
	}
}
