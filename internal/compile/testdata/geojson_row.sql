-- request: GET /api/places/1?select=id,name,location
SELECT json_build_object('etag', __q.h || '.' || md5('application/geo+json' || __q.data::text), 'row', __q.data) AS data
FROM (
  SELECT md5((json_build_object('id', t0."id", 'owner_id', t0."owner_id", 'name', t0."name", 'location', ST_AsGeoJSON(t0."location")::json, 'area', ST_AsGeoJSON(t0."area")::json))::text) AS h, json_build_object('type', 'Feature', 'geometry', ST_AsGeoJSON(t0."location")::json, 'properties', json_build_object('id', t0."id", 'name', t0."name")) AS data
  FROM "public"."places" t0
  WHERE t0."id" = $1
  LIMIT 1
) __q
-- args: [1]
