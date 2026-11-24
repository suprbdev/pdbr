-- request: GET /api/places/1?select=name,~area_st_distance&~near=%7B%22type%22%3A%22Point%22%2C%22coordinates%22%3A%5B0%2C0%5D%7D
SELECT json_build_object('etag', __q.h || '.' || md5(__q.data::text), 'row', __q.data) AS data
FROM (
  SELECT md5((json_build_object('id', t0."id", 'owner_id', t0."owner_id", 'name', t0."name", 'location', ST_AsGeoJSON(t0."location")::json, 'area', ST_AsGeoJSON(t0."area")::json))::text) AS h, json_build_object('name', t0."name", '~area_st_distance', ST_Distance(t0."area", (CASE WHEN left($1, 1) = '{' THEN ST_GeomFromGeoJSON($1) ELSE ST_GeomFromEWKT($1) END)::geography)) AS data
  FROM "public"."places" t0
  WHERE t0."id" = $2
  LIMIT 1
) __q
-- args: ["{\"coordinates\":[0,0],\"type\":\"Point\"}",1]
