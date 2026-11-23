-- request: GET /api/users?distinct=mood&order=mood.asc&select=mood&limit=10
SELECT json_build_object('has_next', coalesce(bool_or(s2.__rn > $1), false), 'has_prev', false, 'n', count(*) FILTER (WHERE s2.__rn <= $1), 'rows', coalesce(json_agg(s2.data ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $1), '[]'::json)) AS data
FROM (
  SELECT d1.*, row_number() OVER (ORDER BY d1.__rn0) AS __rn
  FROM (
    SELECT DISTINCT ON (t0."mood") json_build_object('mood', t0."mood") AS data, row_number() OVER (ORDER BY t0."mood" ASC, t0."id" ASC) AS __rn0
    FROM "public"."users" t0
    ORDER BY t0."mood" ASC, t0."id" ASC
    LIMIT 11
  ) d1
) s2
-- args: [10]
