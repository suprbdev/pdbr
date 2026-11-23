-- request: GET /api/comments?select=n:count(),...posts(published,...users(mood))&posts.users.mood=neq.sad
SELECT json_build_object('has_next', coalesce(bool_or(s4.__rn > $3), false), 'has_prev', false, 'n', count(*) FILTER (WHERE s4.__rn <= $3), 'rows', coalesce(json_agg(s4.data ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $3), '[]'::json)) AS data
FROM (
  SELECT json_build_object($1::text, to_json(g1."__a0"), 'published', g1."__k0", 'mood', g1."__k1") AS data, row_number() OVER (ORDER BY g1."__k0" ASC, g1."__k1" ASC) AS __rn
  FROM (
    SELECT count(*) AS __a0, t2."published" AS __k0, t3."mood" AS __k1
    FROM "public"."comments" t0
    LEFT JOIN "public"."posts" t2 ON t2."id" = t0."post_id"
    LEFT JOIN "public"."users" t3 ON t3."id" = t2."author_id" AND t3."mood" <> $2
    GROUP BY 2, 3
  ) g1
  ORDER BY g1."__k0" ASC, g1."__k1" ASC
  LIMIT 101
) s4
-- args: ["n","sad",100]
