-- request: GET /api/places?select=id,pos:location(transform=3857)&order=id
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $1), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $1))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $1))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $1), 'rows', json_build_object('type', 'FeatureCollection', 'features', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $1), '[]'::json))) AS data
FROM (
  SELECT json_build_object('type', 'Feature', 'geometry', ST_AsGeoJSON(ST_Transform(t0."location", 3857))::json, 'properties', json_build_object('id', t0."id")) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."places" t0
  ORDER BY t0."id" ASC
  LIMIT 101
) s1
-- args: [100]
