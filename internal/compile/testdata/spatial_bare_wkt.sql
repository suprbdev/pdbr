-- request: GET /api/places?select=id&location=st_dwithin.("POINT(-0.1281%2051.508)",0.01)&order=location.dist,id&near=POINT(-0.1281%2051.508)&limit=3
SELECT jsonb_build_object('rows', coalesce(jsonb_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $5), '[]'::jsonb), 'has_next', coalesce(bool_or(s1.__rn > $5), false), 'has_prev', false, 'first', (jsonb_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $5))->0, 'last', (jsonb_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $5))->-1) AS data
FROM (
  SELECT jsonb_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."location" <-> ST_SetSRID(ST_GeomFromEWKT($1), 4326) ASC, t0."id" ASC) AS __rn
  FROM "public"."places" t0
  WHERE ST_DWithin(t0."location", ST_SetSRID(ST_GeomFromEWKT($2), 4326), $3)
  ORDER BY t0."location" <-> ST_SetSRID(ST_GeomFromEWKT($1), 4326) ASC, t0."id" ASC
  LIMIT $4
) s1
-- args: ["POINT(-0.1281 51.508)","POINT(-0.1281 51.508)",0.01,4,3]
