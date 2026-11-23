-- request: GET /api/posts?select=author_id,count(),id.max()&order=author_id.desc&limit=2
SELECT jsonb_build_object('rows', coalesce(jsonb_agg(s2.data ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $2), '[]'::jsonb), 'has_next', coalesce(bool_or(s2.__rn > $2), false), 'has_prev', false) AS data
FROM (
  SELECT jsonb_build_object('author_id', g1."__k0", 'count', to_jsonb(g1."__a0"), 'max', to_jsonb(g1."__a1")) AS data, row_number() OVER (ORDER BY g1."__k0" DESC) AS __rn
  FROM (
    SELECT t0."author_id" AS __k0, count(*) AS __a0, max(t0."id") AS __a1
    FROM "public"."posts" t0
    GROUP BY 1
  ) g1
  ORDER BY g1."__k0" DESC
  LIMIT $1
) s2
-- args: [3,2]
