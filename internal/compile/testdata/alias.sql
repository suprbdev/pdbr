-- request: GET /api/posts?select=id,headline:title,author:users(email)&order=title.desc&limit=3
SELECT json_build_object('has_next', coalesce(bool_or(s3.__rn > $2), false), 'has_prev', false, 'first', (json_agg(s3.__key ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $2))->0, 'last', (json_agg(s3.__key ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $2))->-1, 'n', count(*) FILTER (WHERE s3.__rn <= $2), 'rows', coalesce(json_agg(s3.data ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $2), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'headline', t0."title", 'author', l2.data) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."title" DESC, t0."id" ASC) AS __rn
  FROM "public"."posts" t0
  LEFT JOIN LATERAL (
    SELECT json_build_object('email', t1."email") AS data
    FROM "public"."users" t1
    WHERE t1."id" = t0."author_id"
    LIMIT 1
  ) l2 ON true
  ORDER BY t0."title" DESC, t0."id" ASC
  LIMIT $1
) s3
-- args: [4,3]
