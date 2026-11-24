-- request: POST /api/rpc/vol_posts?limit=0&count=true
WITH __call AS MATERIALIZED (SELECT * FROM "public"."vol_posts"())
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $1), false), 'has_prev', false, 'total', (SELECT count(*) FROM __call c2), 'n', count(*) FILTER (WHERE s1.__rn <= $1), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $1), '[]'::json)) AS data
FROM (
  SELECT NULL::json AS data, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM __call t0
  ORDER BY t0."id" ASC
  LIMIT 1
) s1
-- args: [0]
