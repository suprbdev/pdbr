-- request: GET /api/posts?select=count()
SELECT json_build_object('has_next', coalesce(bool_or(s2.__rn > $1), false), 'has_prev', false, 'n', count(*) FILTER (WHERE s2.__rn <= $1), 'rows', coalesce(json_agg(s2.data ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $1), '[]'::json)) AS data
FROM (
  SELECT json_build_object('count', to_json(g1."__a0")) AS data, row_number() OVER () AS __rn
  FROM (
    SELECT count(*) AS __a0
    FROM "public"."posts" t0
  ) g1
  LIMIT 101
) s2
-- args: [100]
