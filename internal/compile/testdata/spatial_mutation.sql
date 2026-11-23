-- request: POST /api/places?select=id,name,location,area,location_srid
WITH __mut AS (
  INSERT INTO "public"."places" ("owner_id", "name", "location", "area")
  VALUES ($1, $2, CASE WHEN left($3, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($3), 4326) ELSE ST_GeomFromEWKT($3) END, (CASE WHEN left($4, 1) = '{' THEN ST_GeomFromGeoJSON($4) ELSE ST_GeomFromEWKT($4) END)::geography)
  RETURNING "id", "name", "location", "area"
)
SELECT jsonb_build_object('row', jsonb_build_object('id', t0."id", 'name', t0."name", 'location', ST_AsGeoJSON(t0."location")::jsonb, 'area', ST_AsGeoJSON(t0."area")::jsonb, 'location_srid', ST_SRID(t0."location")), 'key', jsonb_build_array(t0."id")) AS data
FROM __mut t0
-- args: [1,"Golden place","{\"coordinates\":[-0.1281,51.508],\"type\":\"Point\"}","SRID=4326;POLYGON((-0.130 51.507, -0.126 51.507, -0.126 51.509, -0.130 51.509, -0.130 51.507))"]
