-- request: GET /api/users?select=mood::text,count()
SELECT json_build_object('has_next', coalesce(bool_or(s2.__rn > $1), false), 'has_prev', false, 'n', count(*) FILTER (WHERE s2.__rn <= $1), 'rows', coalesce(json_agg(s2.data ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $1), '[]'::json)) AS data
FROM (
  SELECT json_build_object('mood', to_json(g1."__k0"), 'count', to_json(g1."__a0")) AS data, row_number() OVER (ORDER BY g1."__k0" ASC) AS __rn
  FROM (
    SELECT (t0."mood")::"pg_catalog"."text" AS __k0, count(*) AS __a0
    FROM "public"."users" t0
    GROUP BY 1
  ) g1
  ORDER BY g1."__k0" ASC
  LIMIT 101
) s2
-- args: [100]
