-- request: GET /api/rpc/search_posts?term=rest&author_id=eq.1&order=id.desc&limit=20&count=true&select=id,title,users(email)
SELECT json_build_object('has_next', coalesce(bool_or(s3.__rn > $4), false), 'has_prev', false, 'total', (SELECT count(*) FROM "public"."search_posts"("term" => $1::"text") c4 WHERE c4."author_id" = $5), 'n', count(*) FILTER (WHERE s3.__rn <= $4), 'rows', coalesce(json_agg(s3.data ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'title', t0."title", 'users', l2.data) AS data, row_number() OVER (ORDER BY t0."id" DESC) AS __rn
  FROM "public"."search_posts"("term" => $1::"text") t0
  LEFT JOIN LATERAL (
    SELECT json_build_object('email', t1."email") AS data
    FROM "public"."users" t1
    WHERE t1."id" = t0."author_id"
    LIMIT 1
  ) l2 ON true
  WHERE t0."author_id" = $2
  ORDER BY t0."id" DESC
  LIMIT $3
) s3
-- args: ["rest",1,21,20,1]
