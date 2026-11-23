-- request: GET /api/users?select=id,email,email:full_name,posts(id),posts:mood&limit=2
SELECT json_build_object('rows', coalesce(json_agg(s4.data ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $3), '[]'::json), 'has_next', coalesce(bool_or(s4.__rn > $3), false), 'has_prev', false, 'first', (json_agg(s4.__key ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $3))->0, 'last', (json_agg(s4.__key ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $3))->-1) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'email', t0."full_name", 'posts', l3.data) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."users" t0
  LEFT JOIN LATERAL (
    SELECT coalesce(json_agg(s2.data), '[]'::json) AS data
    FROM (
      SELECT json_build_object('id', t1."id") AS data
      FROM "public"."posts" t1
      WHERE t1."author_id" = t0."id"
      ORDER BY t1."id" ASC
      LIMIT $1
    ) s2
  ) l3 ON true
  ORDER BY t0."id" ASC
  LIMIT $2
) s4
-- args: [100,3,2]
