-- request: GET /api/posts?users=exists.false&select=id
SELECT jsonb_build_object('rows', coalesce(jsonb_agg(s2.data ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $2), '[]'::jsonb), 'has_next', coalesce(bool_or(s2.__rn > $2), false), 'has_prev', false, 'first', (jsonb_agg(s2.__key ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $2))->0, 'last', (jsonb_agg(s2.__key ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $2))->-1) AS data
FROM (
  SELECT jsonb_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."posts" t0
  WHERE NOT EXISTS (SELECT 1 FROM "public"."users" r1 WHERE r1."id" = t0."author_id")
  ORDER BY t0."id" ASC
  LIMIT $1
) s2
-- args: [101,100]
