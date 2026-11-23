-- request: GET /api/metrics?limit=20&offset=40
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $2), false), 'has_prev', true, 'n', count(*) FILTER (WHERE s1.__rn <= $2), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $2), '[]'::json)) AS data
FROM (
  SELECT json_build_object('name', t0."name", 'value', t0."value") AS data, row_number() OVER () AS __rn
  FROM "public"."metrics" t0
  LIMIT 21
  OFFSET $1
) s1
-- args: [40,60]
