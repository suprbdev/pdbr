-- request: GET /api/posts?select=id.count(),users(mood)
SELECT jsonb_build_object('rows', coalesce(jsonb_agg(s4.data ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $2), '[]'::jsonb), 'has_next', coalesce(bool_or(s4.__rn > $2), false), 'has_prev', false) AS data
FROM (
  SELECT jsonb_build_object('count', to_jsonb(g1."__a0"), 'users', g1."__k0") AS data, row_number() OVER (ORDER BY g1."__k0" ASC) AS __rn
  FROM (
    SELECT count(t0."id") AS __a0, l3.data AS __k0
    FROM "public"."posts" t0
    LEFT JOIN LATERAL (
      SELECT jsonb_build_object('mood', t2."mood") AS data
      FROM "public"."users" t2
      WHERE t2."id" = t0."author_id"
      LIMIT 1
    ) l3 ON true
    GROUP BY 2
  ) g1
  ORDER BY g1."__k0" ASC
  LIMIT $1
) s4
-- args: [101,100]
