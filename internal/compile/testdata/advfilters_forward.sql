-- request: GET /api/posts?users.email=eq.ada@example.com&users=exists.true&select=id&order=users(email).desc,id&limit=2
SELECT jsonb_build_object('rows', coalesce(jsonb_agg(s3.data ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $3), '[]'::jsonb), 'has_next', coalesce(bool_or(s3.__rn > $3), false), 'has_prev', false, 'first', (jsonb_agg(s3.__key ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $3))->0, 'last', (jsonb_agg(s3.__key ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $3))->-1) AS data
FROM (
  SELECT jsonb_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY (SELECT __ro."email" FROM "public"."users" __ro WHERE __ro."id" = t0."author_id") DESC NULLS LAST, t0."id" ASC) AS __rn
  FROM "public"."posts" t0
  WHERE EXISTS (SELECT 1 FROM "public"."users" r1 WHERE r1."id" = t0."author_id") AND EXISTS (SELECT 1 FROM "public"."users" r2 WHERE r2."id" = t0."author_id" AND (r2."email" = $1))
  ORDER BY (SELECT __ro."email" FROM "public"."users" __ro WHERE __ro."id" = t0."author_id") DESC NULLS LAST, t0."id" ASC
  LIMIT $2
) s3
-- args: ["ada@example.com",3,2]
