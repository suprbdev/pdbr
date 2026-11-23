-- request: GET /api/places?select=id,location,users(id,email)&order=id
SELECT json_build_object('has_next', coalesce(bool_or(s3.__rn > $1), false), 'has_prev', false, 'first', (json_agg(s3.__key ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $1))->0, 'last', (json_agg(s3.__key ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $1))->-1, 'n', count(*) FILTER (WHERE s3.__rn <= $1), 'rows', json_build_object('type', 'FeatureCollection', 'features', coalesce(json_agg(s3.data ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $1), '[]'::json))) AS data
FROM (
  SELECT json_build_object('type', 'Feature', 'geometry', ST_AsGeoJSON(t0."location")::json, 'properties', json_build_object('id', t0."id", 'users', l2.data)) AS data, jsonb_build_array(t0."id") AS __key, t0.__rn
  FROM (
    SELECT t0."id", t0."owner_id", t0."location", row_number() OVER (ORDER BY t0."id" ASC) AS __rn
    FROM "public"."places" t0
    ORDER BY t0."id" ASC
    LIMIT 101
  ) t0
  LEFT JOIN LATERAL (
    SELECT json_build_object('id', t1."id", 'email', t1."email") AS data
    FROM "public"."users" t1
    WHERE t1."id" = t0."owner_id"
    LIMIT 1
  ) l2 ON true
) s3
-- args: [100]
