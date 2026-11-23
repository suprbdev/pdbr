-- request: GET /api/users?select=id,posts(count)
SELECT jsonb_build_object('rows', coalesce(jsonb_agg(s5.data ORDER BY s5.__rn ASC) FILTER (WHERE s5.__rn <= $3), '[]'::jsonb), 'has_next', coalesce(bool_or(s5.__rn > $3), false), 'has_prev', false, 'first', (jsonb_agg(s5.__key ORDER BY s5.__rn ASC) FILTER (WHERE s5.__rn <= $3))->0, 'last', (jsonb_agg(s5.__key ORDER BY s5.__rn ASC) FILTER (WHERE s5.__rn <= $3))->-1) AS data
FROM (
  SELECT jsonb_build_object('id', t0."id", 'posts', l4.data) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."users" t0
  LEFT JOIN LATERAL (
    SELECT coalesce(jsonb_agg(s3.data), '[]'::jsonb) AS data
    FROM (
      SELECT jsonb_build_object('count', to_jsonb(g2."__a0")) AS data
      FROM (
        SELECT count(*) AS __a0
        FROM "public"."posts" t1
        WHERE t1."author_id" = t0."id"
      ) g2
      LIMIT $1
    ) s3
  ) l4 ON true
  ORDER BY t0."id" ASC
  LIMIT $2
) s5
-- args: [100,101,100]
