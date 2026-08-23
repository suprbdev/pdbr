-- request: GET /api/metrics?limit=20&offset=40
SELECT jsonb_build_object('rows', coalesce(jsonb_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $3), '[]'::jsonb), 'has_next', coalesce(bool_or(s1.__rn > $3), false), 'has_prev', true) AS data
FROM (
  SELECT jsonb_build_object('name', t0."name", 'value', t0."value") AS data, row_number() OVER () AS __rn
  FROM "public"."metrics" t0
  LIMIT $1
  OFFSET $2
) s1
-- args: [21,40,60]
