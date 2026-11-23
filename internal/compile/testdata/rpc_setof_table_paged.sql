-- request: GET /api/rpc/search_posts?term=rest&author_id=eq.1&order=id.desc&limit=20&count=true&select=id,title,users(email)
SELECT json_build_object('has_next', coalesce(bool_or(s3.__rn > $3), false), 'has_prev', false, 'total', (SELECT count(*) FROM "public"."search_posts"("term" => $1::"text") c4 WHERE c4."author_id" = $4), 'n', count(*) FILTER (WHERE s3.__rn <= $3), 'rows', coalesce(json_agg(s3.data ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $3), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'title', t0."title", 'users', l2.data) AS data, t0.__rn
  FROM (
    SELECT t0."id", t0."author_id", t0."title", row_number() OVER (ORDER BY t0."id" DESC) AS __rn
    FROM "public"."search_posts"("term" => $1::"text") t0
    WHERE t0."author_id" = $2
    ORDER BY t0."id" DESC
    LIMIT 21
  ) t0
  LEFT JOIN LATERAL (
    SELECT json_build_object('email', t1."email") AS data
    FROM "public"."users" t1
    WHERE t1."id" = t0."author_id"
    LIMIT 1
  ) l2 ON true
) s3
-- args: ["rest",1,20,1]
