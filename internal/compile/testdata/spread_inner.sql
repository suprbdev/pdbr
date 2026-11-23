-- request: GET /api/posts?select=id,...users!inner(email)&users.mood=eq.happy
SELECT json_build_object('has_next', coalesce(bool_or(s4.__rn > $4), false), 'has_prev', false, 'first', (json_agg(s4.__key ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $4))->0, 'last', (json_agg(s4.__key ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $4))->-1, 'n', count(*) FILTER (WHERE s4.__rn <= $4), 'rows', coalesce(json_agg(s4.data ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'email', l1.v0) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."posts" t0
  LEFT JOIN LATERAL (
    SELECT t2."email" AS v0
    FROM "public"."users" t2
    WHERE t2."id" = t0."author_id" AND t2."mood" = $1
    LIMIT 1
  ) l1 ON true
  WHERE EXISTS (SELECT 1 FROM "public"."users" r3 WHERE r3."id" = t0."author_id" AND r3."mood" = $2)
  ORDER BY t0."id" ASC
  LIMIT $3
) s4
-- args: ["happy","happy",101,100]
