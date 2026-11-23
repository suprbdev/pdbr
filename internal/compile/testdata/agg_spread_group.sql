-- request: GET /api/posts?select=id.count(),...users(mood)&count=true
SELECT json_build_object('has_next', coalesce(bool_or(s3.__rn > $1), false), 'has_prev', false, 'total', (SELECT count(*) FROM (SELECT 1 FROM "public"."posts" c4 LEFT JOIN "public"."users" c5 ON c5."id" = c4."author_id" GROUP BY c5."mood") g6), 'n', count(*) FILTER (WHERE s3.__rn <= $1), 'rows', coalesce(json_agg(s3.data ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $1), '[]'::json)) AS data
FROM (
  SELECT json_build_object('count', to_json(g1."__a0"), 'mood', g1."__k0") AS data, row_number() OVER (ORDER BY g1."__k0" ASC) AS __rn
  FROM (
    SELECT count(t0."id") AS __a0, t2."mood" AS __k0
    FROM "public"."posts" t0
    LEFT JOIN "public"."users" t2 ON t2."id" = t0."author_id"
    GROUP BY 2
  ) g1
  ORDER BY g1."__k0" ASC
  LIMIT 101
) s3
-- args: [100]
