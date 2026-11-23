-- request: GET /api/places/1?select=id,name,location
SELECT json_build_object('etag', md5(to_jsonb(ROW(t0."id", t0."owner_id", t0."name", t0."location", t0."area"))::text), 'row', json_build_object('type', 'Feature', 'geometry', ST_AsGeoJSON(t0."location")::json, 'properties', json_build_object('id', t0."id", 'name', t0."name"))) AS data
FROM "public"."places" t0
WHERE t0."id" = $1
LIMIT 1
-- args: [1]
