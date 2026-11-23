-- request: GET /api/posts?select=published,...users(newest:created_at.max(),n:count())
SELECT json_build_object('has_next', coalesce(bool_or(s3.__rn > $2), false), 'has_prev', false, 'n', count(*) FILTER (WHERE s3.__rn <= $2), 'rows', coalesce(json_agg(s3.data ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $2), '[]'::json)) AS data
FROM (
  SELECT json_build_object('published', g1."__k0", 'newest', to_json(g1."__a0"), 'n', to_json(g1."__a1")) AS data, row_number() OVER (ORDER BY g1."__k0" ASC) AS __rn
  FROM (
    SELECT t0."published" AS __k0, max(t2."created_at") AS __a0, count(*) AS __a1
    FROM "public"."posts" t0
    LEFT JOIN "public"."users" t2 ON t2."id" = t0."author_id"
    GROUP BY 1
  ) g1
  ORDER BY g1."__k0" ASC
  LIMIT $1
) s3
-- args: [101,100]
