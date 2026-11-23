-- request: GET /api/posts?select=author_id,count()&limit=2&offset=1
SELECT json_build_object('has_next', coalesce(bool_or(s2.__rn > $3), false), 'has_prev', true, 'total', (SELECT count(*) FROM (SELECT 1 FROM "public"."posts" c3 GROUP BY c3."author_id") g4), 'n', count(*) FILTER (WHERE s2.__rn <= $3), 'rows', coalesce(json_agg(s2.data ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $3), '[]'::json)) AS data
FROM (
  SELECT json_build_object('author_id', g1."__k0", 'count', to_json(g1."__a0")) AS data, row_number() OVER (ORDER BY g1."__k0" ASC) AS __rn
  FROM (
    SELECT t0."author_id" AS __k0, count(*) AS __a0
    FROM "public"."posts" t0
    GROUP BY 1
  ) g1
  ORDER BY g1."__k0" ASC
  LIMIT $1
  OFFSET $2
) s2
-- args: [3,1,3]
