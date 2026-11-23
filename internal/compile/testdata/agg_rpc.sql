-- request: GET /api/rpc/search_posts?term=o&select=author_id,count()&order=author_id
SELECT json_build_object('rows', coalesce(json_agg(s2.data ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $3), '[]'::json), 'has_next', coalesce(bool_or(s2.__rn > $3), false), 'has_prev', false) AS data
FROM (
  SELECT json_build_object('author_id', g1."__k0", 'count', to_json(g1."__a0")) AS data, row_number() OVER (ORDER BY g1."__k0" ASC) AS __rn
  FROM (
    SELECT t0."author_id" AS __k0, count(*) AS __a0
    FROM "public"."search_posts"("term" => $1::"text") t0
    GROUP BY 1
  ) g1
  ORDER BY g1."__k0" ASC
  LIMIT $2
) s2
-- args: ["o",101,100]
