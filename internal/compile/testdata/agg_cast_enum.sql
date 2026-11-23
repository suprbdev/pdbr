-- request: GET /api/users?select=mood.max()::mood,m:mood::text.min(),day:created_at::date.max(),n:count()
SELECT json_build_object('has_next', coalesce(bool_or(s2.__rn > $4), false), 'has_prev', false, 'n', count(*) FILTER (WHERE s2.__rn <= $4), 'rows', coalesce(json_agg(s2.data ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('max', to_json((g1."__a0")::"public"."mood"), $1::text, to_json(g1."__a1"), $2::text, to_json(g1."__a2"), $3::text, to_json(g1."__a3")) AS data, row_number() OVER () AS __rn
  FROM (
    SELECT max(t0."mood") AS __a0, min((t0."mood")::"pg_catalog"."text") AS __a1, max((t0."created_at")::"pg_catalog"."date") AS __a2, count(*) AS __a3
    FROM "public"."users" t0
  ) g1
  LIMIT 101
) s2
-- args: ["m","day","n",100]
