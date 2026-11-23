-- request: GET /api/comments?select=id,...posts(post:title,...users(author:email),...comments(replies:id))
SELECT json_build_object('has_next', coalesce(bool_or(s8.__rn > $3), false), 'has_prev', false, 'first', (json_agg(s8.__key ORDER BY s8.__rn ASC) FILTER (WHERE s8.__rn <= $3))->0, 'last', (json_agg(s8.__key ORDER BY s8.__rn ASC) FILTER (WHERE s8.__rn <= $3))->-1, 'n', count(*) FILTER (WHERE s8.__rn <= $3), 'rows', coalesce(json_agg(s8.data ORDER BY s8.__rn ASC) FILTER (WHERE s8.__rn <= $3), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'post', l1.v0, 'author', l1.v1, 'replies', l1.v2) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."comments" t0
  LEFT JOIN LATERAL (
    SELECT t2."title" AS v0, l3.v0 AS v1, l5.v0 AS v2
    FROM "public"."posts" t2
    LEFT JOIN LATERAL (
      SELECT t4."email" AS v0
      FROM "public"."users" t4
      WHERE t4."id" = t2."author_id"
      LIMIT 1
    ) l3 ON true
    LEFT JOIN LATERAL (
      SELECT coalesce(json_agg(s7.v0), '[]'::json) AS v0
      FROM (
        SELECT t6."id" AS v0
        FROM "public"."comments" t6
        WHERE t6."post_id" = t2."id"
        ORDER BY t6."id" ASC
        LIMIT $1
      ) s7
    ) l5 ON true
    WHERE t2."id" = t0."post_id"
    LIMIT 1
  ) l1 ON true
  ORDER BY t0."id" ASC
  LIMIT $2
) s8
-- args: [100,101,100]
