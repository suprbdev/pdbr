-- request: GET /api/posts?select=title,users!inner(email)&users.email=eq.a@b.c
SELECT json_build_object('rows', coalesce(json_agg(s4.data ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $4), '[]'::json), 'has_next', coalesce(bool_or(s4.__rn > $4), false), 'has_prev', false, 'first', (json_agg(s4.__key ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $4))->0, 'last', (json_agg(s4.__key ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $4))->-1) AS data
FROM (
  SELECT json_build_object('title', t0."title", 'users', l2.data) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."posts" t0
  LEFT JOIN LATERAL (
    SELECT json_build_object('email', t1."email") AS data
    FROM "public"."users" t1
    WHERE t1."id" = t0."author_id" AND t1."email" = $1
    LIMIT 1
  ) l2 ON true
  WHERE EXISTS (SELECT 1 FROM "public"."users" r3 WHERE r3."id" = t0."author_id" AND r3."email" = $2)
  ORDER BY t0."id" ASC
  LIMIT $3
) s4
-- args: ["a@b.c","a@b.c",101,100]
