-- request: GET /api/posts?select=author_id,count()&limit=2&offset=1
SELECT jsonb_build_object('rows', coalesce(jsonb_agg(s2.data ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $3), '[]'::jsonb), 'has_next', coalesce(bool_or(s2.__rn > $3), false), 'has_prev', true, 'total', (SELECT count(*) FROM (SELECT t3."author_id" AS __k0, count(*) AS __a0 FROM "public"."posts" t3 GROUP BY 1) g4)) AS data
FROM (
  SELECT jsonb_build_object('author_id', g1."__k0", 'count', to_jsonb(g1."__a0")) AS data, row_number() OVER (ORDER BY g1."__k0" ASC) AS __rn
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
