-- request: GET /api/users?select=mood,total:balance.sum(),mean:id::float8.avg(),newest:created_at.max()::text,id.min()&mood=in.(happy,ok)
SELECT json_build_object('has_next', coalesce(bool_or(s2.__rn > $5), false), 'has_prev', false, 'n', count(*) FILTER (WHERE s2.__rn <= $5), 'rows', coalesce(json_agg(s2.data ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $5), '[]'::json)) AS data
FROM (
  SELECT json_build_object('mood', g1."__k0", $1::text, to_json(g1."__a0"), $2::text, to_json(g1."__a1"), $3::text, to_json((g1."__a2")::"pg_catalog"."text"), 'min', to_json(g1."__a3")) AS data, row_number() OVER (ORDER BY g1."__k0" ASC) AS __rn
  FROM (
    SELECT t0."mood" AS __k0, sum(t0."balance") AS __a0, avg((t0."id")::"pg_catalog"."float8") AS __a1, max(t0."created_at") AS __a2, min(t0."id") AS __a3
    FROM "public"."users" t0
    WHERE t0."mood" = ANY($4::text[]::"public"."mood"[])
    GROUP BY 1
  ) g1
  ORDER BY g1."__k0" ASC
  LIMIT 101
) s2
-- args: ["total","mean","newest",["happy","ok"],100]
