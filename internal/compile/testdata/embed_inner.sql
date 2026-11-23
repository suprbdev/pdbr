-- request: GET /api/posts?select=title,users!inner(email)&users.email=eq.a@b.c
SELECT json_build_object('has_next', coalesce(bool_or(s4.__rn > $3), false), 'has_prev', false, 'first', (json_agg(s4.__key ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $3))->0, 'last', (json_agg(s4.__key ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $3))->-1, 'n', count(*) FILTER (WHERE s4.__rn <= $3), 'rows', coalesce(json_agg(s4.data ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $3), '[]'::json)) AS data
FROM (
  SELECT json_build_object('title', t0."title", 'users', l2.data) AS data, jsonb_build_array(t0."id") AS __key, t0.__rn
  FROM (
    SELECT t0."id", t0."author_id", t0."title", row_number() OVER (ORDER BY t0."id" ASC) AS __rn
    FROM "public"."posts" t0
    WHERE EXISTS (SELECT 1 FROM "public"."users" r3 WHERE r3."id" = t0."author_id" AND r3."email" = $2)
    ORDER BY t0."id" ASC
    LIMIT 101
  ) t0
  LEFT JOIN LATERAL (
    SELECT json_build_object('email', t1."email") AS data
    FROM "public"."users" t1
    WHERE t1."id" = t0."author_id" AND t1."email" = $1
    LIMIT 1
  ) l2 ON true
) s4
-- args: ["a@b.c","a@b.c",100]
