-- request: GET /api/places?select=id&location=bbox_intersects2d.SRID%3D4326%3BLINESTRING(-1%2051,1%2052)&area=not.bbox_intersects2d.SRID%3D4326%3BPOINT(10%2010)&or=(area.bbox_intersects2d."{\"type\":\"Point\",\"coordinates\":[-0.128,51.508]}",location.is.null)&order=id
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $4), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $4), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."places" t0
  WHERE NOT (t0."area" && (CASE WHEN left($1, 1) = '{' THEN ST_GeomFromGeoJSON($1) ELSE ST_GeomFromEWKT($1) END)::geography) AND t0."location" && CASE WHEN left($2, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($2), 4326) ELSE ST_GeomFromEWKT($2) END AND (t0."area" && (CASE WHEN left($3, 1) = '{' THEN ST_GeomFromGeoJSON($3) ELSE ST_GeomFromEWKT($3) END)::geography OR t0."location" IS NULL)
  ORDER BY t0."id" ASC
  LIMIT 101
) s1
-- args: ["SRID=4326;POINT(10 10)","SRID=4326;LINESTRING(-1 51,1 52)","{\"coordinates\":[-0.128,51.508],\"type\":\"Point\"}",100]
