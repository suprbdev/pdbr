-- request: GET /api/places?select=id&or=(location.bbox_leftof."SRID=4326;POINT(0%200)",location.bbox_rightof."SRID=4326;POINT(100%200)",location.bbox_below."SRID=4326;POINT(0%2040)",location.bbox_above."SRID=4326;POINT(0%2060)")&and=(location.bbox_overlapsorleftof."SRID=4326;POINT(1%200)",location.bbox_overlapsorrightof."SRID=4326;POINT(-1%200)",not.location.bbox_overlapsorbelow."SRID=4326;POINT(0%20-90)",location.bbox_overlapsorabove."SRID=4326;POINT(0%20-90)")&area=bbox_overlapsorleftof.SRID%3D4326%3BPOINT(0%200)&order=id
SELECT jsonb_build_object('rows', coalesce(jsonb_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $11), '[]'::jsonb), 'has_next', coalesce(bool_or(s1.__rn > $11), false), 'has_prev', false, 'first', (jsonb_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $11))->0, 'last', (jsonb_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $11))->-1) AS data
FROM (
  SELECT jsonb_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."places" t0
  WHERE (t0."location" |&> ST_GeomFromEWKT($1) AND NOT (t0."location" &<| ST_GeomFromEWKT($2)) AND t0."location" &< ST_GeomFromEWKT($3) AND t0."location" &> ST_GeomFromEWKT($4)) AND t0."area"::geometry &< ST_GeomFromEWKT($5) AND (t0."location" |>> ST_GeomFromEWKT($6) OR t0."location" <<| ST_GeomFromEWKT($7) OR t0."location" << ST_GeomFromEWKT($8) OR t0."location" >> ST_GeomFromEWKT($9))
  ORDER BY t0."id" ASC
  LIMIT $10
) s1
-- args: ["SRID=4326;POINT(0 -90)","SRID=4326;POINT(0 -90)","SRID=4326;POINT(1 0)","SRID=4326;POINT(-1 0)","SRID=4326;POINT(0 0)","SRID=4326;POINT(0 60)","SRID=4326;POINT(0 40)","SRID=4326;POINT(0 0)","SRID=4326;POINT(100 0)",101,100]
