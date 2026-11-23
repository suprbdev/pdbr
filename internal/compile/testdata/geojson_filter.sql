-- request: GET /api/places?select=id,location&location=bbox_intersects.(-1,51,1,52)&order=id
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $5), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $5))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $5))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $5), 'rows', json_build_object('type', 'FeatureCollection', 'features', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $5), '[]'::json))) AS data
FROM (
  SELECT json_build_object('type', 'Feature', 'geometry', ST_AsGeoJSON(t0."location")::json, 'properties', json_build_object('id', t0."id")) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."places" t0
  WHERE t0."location" && ST_MakeEnvelope($1, $2, $3, $4, 4326)
  ORDER BY t0."id" ASC
  LIMIT 101
) s1
-- args: [-1,51,1,52,100]
