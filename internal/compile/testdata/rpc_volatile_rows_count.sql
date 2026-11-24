-- request: POST /api/rpc/vol_posts?limit=2&offset=1&count=true
WITH __call AS MATERIALIZED (SELECT * FROM "public"."vol_posts"())
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $2), false), 'has_prev', true, 'total', (SELECT count(*) FROM __call c2), 'n', count(*) FILTER (WHERE s1.__rn <= $2), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $2), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'author_id', t0."author_id", 'title', t0."title", 'body', t0."body", 'published', t0."published") AS data, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM __call t0
  ORDER BY t0."id" ASC
  LIMIT 3
  OFFSET $1
) s1
-- args: [1,3]
