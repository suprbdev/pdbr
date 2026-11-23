-- request: GET /api/posts?select=id&users.settings->>theme=eq.dark
SELECT json_build_object('has_next', coalesce(bool_or(s2.__rn > $4), false), 'has_prev', false, 'first', (json_agg(s2.__key ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $4))->0, 'last', (json_agg(s2.__key ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $4))->-1, 'n', count(*) FILTER (WHERE s2.__rn <= $4), 'rows', coalesce(json_agg(s2.data ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."posts" t0
  WHERE EXISTS (SELECT 1 FROM "public"."users" r1 WHERE r1."id" = t0."author_id" AND ((r1."settings" ->> $1::text) = $2))
  ORDER BY t0."id" ASC
  LIMIT $3
) s2
-- args: ["theme","dark",101,100]
