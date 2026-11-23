-- request: GET /api/places?select=id,name&location=st_dwithin.("SRID=4326;POINT(-0.1281%2051.508)",5000,true)&area=not.st_beyond.("POINT(-0.128%2051.508)",100)&order=id&limit=10
SELECT json_build_object('rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6), '[]'::json), 'has_next', coalesce(bool_or(s1.__rn > $6), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6))->-1) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'name', t0."name") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."places" t0
  WHERE NOT (NOT ST_DWithin(t0."area", (CASE WHEN left($1, 1) = '{' THEN ST_GeomFromGeoJSON($1) ELSE ST_GeomFromEWKT($1) END)::geography, $2)) AND ST_DWithin(t0."location"::geography, (CASE WHEN left($3, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($3), 4326) ELSE ST_GeomFromEWKT($3) END)::geography, $4)
  ORDER BY t0."id" ASC
  LIMIT $5
) s1
-- args: ["SRID=4326;POINT(-0.128 51.508)",100,"SRID=4326;POINT(-0.1281 51.508)",5000,11,10]
