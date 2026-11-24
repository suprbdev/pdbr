-- request: GET /api/places?select=id&location=st_dwithin.("POINT(-0.1281%2051.508)",0.01)&order=location.st_distance,id&~near=POINT(-0.1281%2051.508)&limit=3
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $4), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $4), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."location" <-> CASE WHEN left($1, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($1), 4326) ELSE ST_GeomFromEWKT($1) END ASC, t0."id" ASC) AS __rn
  FROM "public"."places" t0
  WHERE ST_DWithin(t0."location", CASE WHEN left($2, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($2), 4326) ELSE ST_GeomFromEWKT($2) END, $3)
  ORDER BY t0."location" <-> CASE WHEN left($1, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($1), 4326) ELSE ST_GeomFromEWKT($1) END ASC, t0."id" ASC
  LIMIT 4
) s1
-- args: ["SRID=4326;POINT(-0.1281 51.508)","SRID=4326;POINT(-0.1281 51.508)",0.01,3]
