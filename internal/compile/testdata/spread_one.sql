-- request: GET /api/posts?select=id,title,...users(author:email,mood)
SELECT json_build_object('has_next', coalesce(bool_or(s3.__rn > $3), false), 'has_prev', false, 'first', (json_agg(s3.__key ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $3))->0, 'last', (json_agg(s3.__key ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $3))->-1, 'n', count(*) FILTER (WHERE s3.__rn <= $3), 'rows', coalesce(json_agg(s3.data ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $3), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'title', t0."title", $1::text, l1.v0, 'mood', l1.v1) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."posts" t0
  LEFT JOIN LATERAL (
    SELECT t2."email" AS v0, t2."mood" AS v1
    FROM "public"."users" t2
    WHERE t2."id" = t0."author_id"
    LIMIT 1
  ) l1 ON true
  ORDER BY t0."id" ASC
  LIMIT $2
) s3
-- args: ["author",101,100]
