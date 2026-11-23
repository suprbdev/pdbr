-- request: GET /api/users?select=mood,total:balance.sum(),mean:id::float8.avg(),newest:created_at.max()::text,id.min()&mood=in.(happy,ok)
SELECT jsonb_build_object('rows', coalesce(jsonb_agg(s2.data ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $3), '[]'::jsonb), 'has_next', coalesce(bool_or(s2.__rn > $3), false), 'has_prev', false) AS data
FROM (
  SELECT jsonb_build_object('mood', g1."__k0", 'total', to_jsonb(g1."__a0"), 'mean', to_jsonb(g1."__a1"), 'newest', to_jsonb((g1."__a2")::"pg_catalog"."text"), 'min', to_jsonb(g1."__a3")) AS data, row_number() OVER (ORDER BY g1."__k0" ASC) AS __rn
  FROM (
    SELECT t0."mood" AS __k0, sum(t0."balance") AS __a0, avg((t0."id")::"pg_catalog"."float8") AS __a1, max(t0."created_at") AS __a2, min(t0."id") AS __a3
    FROM "public"."users" t0
    WHERE t0."mood" = ANY($1::text[]::"public"."mood"[])
    GROUP BY 1
  ) g1
  ORDER BY g1."__k0" ASC
  LIMIT $2
) s2
-- args: [["happy","ok"],101,100]
