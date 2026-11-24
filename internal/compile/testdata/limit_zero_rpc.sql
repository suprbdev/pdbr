-- request: GET /api/rpc/search_posts?term=rest&author_id=eq.1&limit=0&offset=2&count=true&select=id,title,users(email)
SELECT json_build_object('has_next', coalesce(bool_or(s3.__rn > $4), false), 'has_prev', true, 'total', (SELECT count(*) FROM "public"."search_posts"("term" => $1::"text") c4 WHERE c4."author_id" = $5), 'n', count(*) FILTER (WHERE s3.__rn <= $4), 'rows', coalesce(json_agg(s3.data ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT NULL::json AS data, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."search_posts"("term" => $1::"text") t0
  WHERE t0."author_id" = $2
  ORDER BY t0."id" ASC
  LIMIT 1
  OFFSET $3
) s3
-- args: ["rest",1,2,2,1]
