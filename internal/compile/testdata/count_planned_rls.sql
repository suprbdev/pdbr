-- request: GET /api/posts?select=id
SELECT jsonb_build_object('rows', coalesce(jsonb_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $2), '[]'::jsonb), 'has_next', coalesce(bool_or(s1.__rn > $2), false), 'has_prev', false, 'first', (jsonb_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $2))->0, 'last', (jsonb_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $2))->-1, 'total', (SELECT count(*) FROM "public"."posts" c2)) AS data
FROM (
  SELECT jsonb_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."posts" t0
  ORDER BY t0."id" ASC
  LIMIT $1
) s1
-- args: [101,100]
