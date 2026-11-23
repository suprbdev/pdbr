-- request: GET /api/kb/tiles?select=id&rast=intersects.SRID%3D4326%3BPOINT(1%202)&or=(rast.not.intersects."{\"type\":\"Point\",\"coordinates\":[3,4]}",rast.is.null)
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $3), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $3))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $3))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $3), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $3), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "kb"."tiles" t0
  WHERE ST_Intersects(t0."rast", CASE WHEN left($1, 1) = '{' THEN ST_GeomFromGeoJSON($1) ELSE ST_GeomFromEWKT($1) END) AND (NOT (ST_Intersects(t0."rast", CASE WHEN left($2, 1) = '{' THEN ST_GeomFromGeoJSON($2) ELSE ST_GeomFromEWKT($2) END)) OR t0."rast" IS NULL)
  ORDER BY t0."id" ASC
  LIMIT 101
) s1
-- args: ["SRID=4326;POINT(1 2)","{\"coordinates\":[3,4],\"type\":\"Point\"}",100]
