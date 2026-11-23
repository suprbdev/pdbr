-- request: GET /api/places?select=id,~location_st_distance,metres:~location_st_distance(spheroid=true),~area_st_distance&order=location.st_distance&~near=SRID%3D4326%3BPOINT(-0.12%2051.5)&limit=3
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $6), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $6), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", '~location_st_distance', ST_Distance(t0."location", CASE WHEN left($1, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($1), 4326) ELSE ST_GeomFromEWKT($1) END), $3::text, ST_Distance((t0."location")::geography, (CASE WHEN left($2, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($2), 4326) ELSE ST_GeomFromEWKT($2) END)::geography), '~area_st_distance', ST_Distance(t0."area", (CASE WHEN left($4, 1) = '{' THEN ST_GeomFromGeoJSON($4) ELSE ST_GeomFromEWKT($4) END)::geography)) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."location" <-> CASE WHEN left($5, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($5), 4326) ELSE ST_GeomFromEWKT($5) END ASC, t0."id" ASC) AS __rn
  FROM "public"."places" t0
  ORDER BY t0."location" <-> CASE WHEN left($5, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($5), 4326) ELSE ST_GeomFromEWKT($5) END ASC, t0."id" ASC
  LIMIT 4
) s1
-- args: ["SRID=4326;POINT(-0.12 51.5)","SRID=4326;POINT(-0.12 51.5)","metres","SRID=4326;POINT(-0.12 51.5)","SRID=4326;POINT(-0.12 51.5)",3]
