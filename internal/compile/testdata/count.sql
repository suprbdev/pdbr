-- request: GET /api/posts?author_id=eq.1&count=true&limit=10
SELECT json_build_object('rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $3), '[]'::json), 'has_next', coalesce(bool_or(s1.__rn > $3), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $3))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $3))->-1, 'total', (SELECT count(*) FROM "public"."posts" c2 WHERE c2."author_id" = $4)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'author_id', t0."author_id", 'title', t0."title", 'body', t0."body", 'published', t0."published") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."posts" t0
  WHERE t0."author_id" = $1
  ORDER BY t0."id" ASC
  LIMIT $2
) s1
-- args: [1,11,10,1]
