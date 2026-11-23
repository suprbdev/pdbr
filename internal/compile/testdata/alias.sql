-- request: GET /api/posts?select=id,headline:title,author:users(email)&order=title.desc&limit=3
SELECT json_build_object('has_next', coalesce(bool_or(s3.__rn > $4), false), 'has_prev', false, 'first', (json_agg(s3.__key ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $4))->0, 'last', (json_agg(s3.__key ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $4))->-1, 'n', count(*) FILTER (WHERE s3.__rn <= $4), 'rows', coalesce(json_agg(s3.data ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", $1::text, t0."title", $2::text, l2.data) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."title" DESC, t0."id" ASC) AS __rn
  FROM "public"."posts" t0
  LEFT JOIN LATERAL (
    SELECT json_build_object('email', t1."email") AS data
    FROM "public"."users" t1
    WHERE t1."id" = t0."author_id"
    LIMIT 1
  ) l2 ON true
  ORDER BY t0."title" DESC, t0."id" ASC
  LIMIT $3
) s3
-- args: ["headline","author",4,3]
