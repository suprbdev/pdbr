-- request: GET /api/posts?select=id,title,users(id,email)
SELECT jsonb_build_object('rows', coalesce(jsonb_agg(s3.data ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $2), '[]'::jsonb), 'has_next', coalesce(bool_or(s3.__rn > $2), false), 'has_prev', false, 'first', (jsonb_agg(s3.__key ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $2))->0, 'last', (jsonb_agg(s3.__key ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $2))->-1) AS data
FROM (
  SELECT jsonb_build_object('id', t0."id", 'title', t0."title", 'users', l2.data) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."posts" t0
  LEFT JOIN LATERAL (
    SELECT jsonb_build_object('id', t1."id", 'email', t1."email") AS data
    FROM "public"."users" t1
    WHERE t1."id" = t0."author_id"
    LIMIT 1
  ) l2 ON true
  ORDER BY t0."id" ASC
  LIMIT $1
) s3
-- args: [101,100]
