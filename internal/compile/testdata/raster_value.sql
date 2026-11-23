-- request: GET /api/kb/tiles?select=id,rast_value,b2:rast_value(band=2),rast_summary_stats,s3:rast_summary_stats(band=3)&near=POINT(1%202)
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $9), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $9))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $9))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $9), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $9), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'rast_value', (SELECT ST_Value(__rv.r, $2::int4, CASE WHEN ST_SRID(__rv.p) = ST_SRID(__rv.r) OR ST_SRID(__rv.p) = 0 OR ST_SRID(__rv.r) = 0 THEN ST_SetSRID(__rv.p, ST_SRID(__rv.r)) ELSE ST_Transform(__rv.p, ST_SRID(__rv.r)) END, true) FROM (SELECT t0."rast" AS r, CASE WHEN left($1, 1) = '{' THEN ST_GeomFromGeoJSON($1) ELSE ST_GeomFromEWKT($1) END AS p) AS __rv), $5::text, (SELECT ST_Value(__rv.r, $4::int4, CASE WHEN ST_SRID(__rv.p) = ST_SRID(__rv.r) OR ST_SRID(__rv.p) = 0 OR ST_SRID(__rv.r) = 0 THEN ST_SetSRID(__rv.p, ST_SRID(__rv.r)) ELSE ST_Transform(__rv.p, ST_SRID(__rv.r)) END, true) FROM (SELECT t0."rast" AS r, CASE WHEN left($3, 1) = '{' THEN ST_GeomFromGeoJSON($3) ELSE ST_GeomFromEWKT($3) END AS p) AS __rv), 'rast_summary_stats', to_json(ST_SummaryStats(t0."rast", $6::int4, true)), $8::text, to_json(ST_SummaryStats(t0."rast", $7::int4, true))) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "kb"."tiles" t0
  ORDER BY t0."id" ASC
  LIMIT 101
) s1
-- args: ["POINT(1 2)",1,"POINT(1 2)",2,"b2",1,3,"s3",100]
