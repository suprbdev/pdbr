-- request: GET /api/users?select=id,posts(count(),...users(mood))
SELECT json_build_object('has_next', coalesce(bool_or(s6.__rn > $3), false), 'has_prev', false, 'first', (json_agg(s6.__key ORDER BY s6.__rn ASC) FILTER (WHERE s6.__rn <= $3))->0, 'last', (json_agg(s6.__key ORDER BY s6.__rn ASC) FILTER (WHERE s6.__rn <= $3))->-1, 'n', count(*) FILTER (WHERE s6.__rn <= $3), 'rows', coalesce(json_agg(s6.data ORDER BY s6.__rn ASC) FILTER (WHERE s6.__rn <= $3), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'posts', l5.data) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."users" t0
  LEFT JOIN LATERAL (
    SELECT coalesce(json_agg(s4.data), '[]'::json) AS data
    FROM (
      SELECT json_build_object('count', to_json(g2."__a0"), 'mood', g2."__k0") AS data
      FROM (
        SELECT count(*) AS __a0, t3."mood" AS __k0
        FROM "public"."posts" t1
        LEFT JOIN "public"."users" t3 ON t3."id" = t1."author_id"
        WHERE t1."author_id" = t0."id"
        GROUP BY 2
      ) g2
      ORDER BY g2."__k0" ASC
      LIMIT $1
    ) s4
  ) l5 ON true
  ORDER BY t0."id" ASC
  LIMIT $2
) s6
-- args: [100,101,100]
