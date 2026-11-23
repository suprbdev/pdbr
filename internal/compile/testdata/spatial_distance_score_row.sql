-- request: GET /api/places/1?select=name,area_distance&near=%7B%22type%22%3A%22Point%22%2C%22coordinates%22%3A%5B0%2C0%5D%7D
SELECT json_build_object('etag', md5(to_jsonb(ROW(t0."id", t0."owner_id", t0."name", t0."location", t0."area"))::text), 'row', json_build_object('name', t0."name", 'area_distance', ST_Distance(t0."area", (CASE WHEN left($1, 1) = '{' THEN ST_GeomFromGeoJSON($1) ELSE ST_GeomFromEWKT($1) END)::geography))) AS data
FROM "public"."places" t0
WHERE t0."id" = $2
LIMIT 1
-- args: ["{\"coordinates\":[0,0],\"type\":\"Point\"}",1]
