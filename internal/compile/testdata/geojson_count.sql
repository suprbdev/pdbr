-- request: GET /api/places?select=id,location&limit=2&offset=1&count=true
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $2), false), 'has_prev', true, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $2))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $2))->-1, 'total', (SELECT count(*) FROM "public"."places" c2), 'n', count(*) FILTER (WHERE s1.__rn <= $2), 'rows', json_build_object('type', 'FeatureCollection', 'features', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $2), '[]'::json))) AS data
FROM (
  SELECT json_build_object('type', 'Feature', 'geometry', ST_AsGeoJSON(t0."location")::json, 'properties', json_build_object('id', t0."id")) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."places" t0
  ORDER BY t0."id" ASC
  LIMIT 3
  OFFSET $1
) s1
-- args: [1,3]
