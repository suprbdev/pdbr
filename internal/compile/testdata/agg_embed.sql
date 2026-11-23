-- request: GET /api/users?select=id,email,posts(author_id,count(),id.avg())&posts.order=author_id.desc&posts.limit=5&limit=10
SELECT json_build_object('has_next', coalesce(bool_or(s5.__rn > $1), false), 'has_prev', false, 'first', (json_agg(s5.__key ORDER BY s5.__rn ASC) FILTER (WHERE s5.__rn <= $1))->0, 'last', (json_agg(s5.__key ORDER BY s5.__rn ASC) FILTER (WHERE s5.__rn <= $1))->-1, 'n', count(*) FILTER (WHERE s5.__rn <= $1), 'rows', coalesce(json_agg(s5.data ORDER BY s5.__rn ASC) FILTER (WHERE s5.__rn <= $1), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'email', t0."email", 'posts', l4.data) AS data, jsonb_build_array(t0."id") AS __key, t0.__rn
  FROM (
    SELECT t0."id", t0."email", row_number() OVER (ORDER BY t0."id" ASC) AS __rn
    FROM "public"."users" t0
    ORDER BY t0."id" ASC
    LIMIT 11
  ) t0
  LEFT JOIN LATERAL (
    SELECT coalesce(json_agg(s3.data), '[]'::json) AS data
    FROM (
      SELECT json_build_object('author_id', g2."__k0", 'count', to_json(g2."__a0"), 'avg', to_json(g2."__a1")) AS data
      FROM (
        SELECT t1."author_id" AS __k0, count(*) AS __a0, avg(t1."id") AS __a1
        FROM "public"."posts" t1
        WHERE t1."author_id" = t0."id"
        GROUP BY 1
      ) g2
      ORDER BY g2."__k0" DESC
      LIMIT 5
    ) s3
  ) l4 ON true
) s5
-- args: [10]
