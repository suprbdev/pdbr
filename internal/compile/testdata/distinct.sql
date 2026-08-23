-- request: GET /api/users?distinct=mood&order=mood.asc&select=mood&limit=10
SELECT jsonb_build_object('rows', coalesce(jsonb_agg(s2.data ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $2), '[]'::jsonb), 'has_next', coalesce(bool_or(s2.__rn > $2), false), 'has_prev', false) AS data
FROM (
  SELECT d1.*, row_number() OVER (ORDER BY d1.__rn0) AS __rn
  FROM (
    SELECT DISTINCT ON (t0."mood") jsonb_build_object('mood', t0."mood") AS data, row_number() OVER (ORDER BY t0."mood" ASC, t0."id" ASC) AS __rn0
    FROM "public"."users" t0
    ORDER BY t0."mood" ASC, t0."id" ASC
    LIMIT $1
  ) d1
) s2
-- args: [11,10]
