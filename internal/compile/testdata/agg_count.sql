-- request: GET /api/posts?select=count()
SELECT jsonb_build_object('rows', coalesce(jsonb_agg(s2.data ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $2), '[]'::jsonb), 'has_next', coalesce(bool_or(s2.__rn > $2), false), 'has_prev', false) AS data
FROM (
  SELECT jsonb_build_object('count', to_jsonb(g1."__a0")) AS data, row_number() OVER () AS __rn
  FROM (
    SELECT count(*) AS __a0
    FROM "public"."posts" t0
  ) g1
  LIMIT $1
) s2
-- args: [101,100]
