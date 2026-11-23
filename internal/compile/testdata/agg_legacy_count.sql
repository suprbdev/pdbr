-- request: GET /api/users?select=id,posts(count)
SELECT json_build_object('has_next', coalesce(bool_or(s5.__rn > $3), false), 'has_prev', false, 'first', (json_agg(s5.__key ORDER BY s5.__rn ASC) FILTER (WHERE s5.__rn <= $3))->0, 'last', (json_agg(s5.__key ORDER BY s5.__rn ASC) FILTER (WHERE s5.__rn <= $3))->-1, 'n', count(*) FILTER (WHERE s5.__rn <= $3), 'rows', coalesce(json_agg(s5.data ORDER BY s5.__rn ASC) FILTER (WHERE s5.__rn <= $3), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'posts', l4.data) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."users" t0
  LEFT JOIN LATERAL (
    SELECT coalesce(json_agg(s3.data), '[]'::json) AS data
    FROM (
      SELECT json_build_object('count', to_json(g2."__a0")) AS data
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
