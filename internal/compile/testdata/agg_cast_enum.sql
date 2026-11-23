-- request: GET /api/users?select=mood.max()::mood,m:mood::text.min(),day:created_at::date.max(),n:count()
SELECT jsonb_build_object('rows', coalesce(jsonb_agg(s2.data ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $2), '[]'::jsonb), 'has_next', coalesce(bool_or(s2.__rn > $2), false), 'has_prev', false) AS data
FROM (
  SELECT jsonb_build_object('max', to_jsonb((g1."__a0")::"public"."mood"), 'm', to_jsonb(g1."__a1"), 'day', to_jsonb(g1."__a2"), 'n', to_jsonb(g1."__a3")) AS data, row_number() OVER () AS __rn
  FROM (
    SELECT max(t0."mood") AS __a0, min((t0."mood")::"pg_catalog"."text") AS __a1, max((t0."created_at")::"pg_catalog"."date") AS __a2, count(*) AS __a3
    FROM "public"."users" t0
  ) g1
  LIMIT $1
) s2
-- args: [101,100]
