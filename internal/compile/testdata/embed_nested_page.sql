-- request: GET /api/users?select=id,posts(id,title,comments(id,body))&posts.limit=3&posts.comments.limit=2&posts.comments.order=id.desc
SELECT json_build_object('has_next', coalesce(bool_or(s7.__rn > $4), false), 'has_prev', false, 'first', (json_agg(s7.__key ORDER BY s7.__rn ASC) FILTER (WHERE s7.__rn <= $4))->0, 'last', (json_agg(s7.__key ORDER BY s7.__rn ASC) FILTER (WHERE s7.__rn <= $4))->-1, 'n', count(*) FILTER (WHERE s7.__rn <= $4), 'rows', coalesce(json_agg(s7.data ORDER BY s7.__rn ASC) FILTER (WHERE s7.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'posts', l6.data) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."users" t0
  LEFT JOIN LATERAL (
    SELECT coalesce(json_agg(s5.data), '[]'::json) AS data
    FROM (
      SELECT json_build_object('id', t1."id", 'title', t1."title", 'comments', l4.data) AS data
      FROM "public"."posts" t1
      LEFT JOIN LATERAL (
        SELECT coalesce(json_agg(s3.data), '[]'::json) AS data
        FROM (
          SELECT json_build_object('id', t2."id", 'body', t2."body") AS data
          FROM "public"."comments" t2
          WHERE t2."post_id" = t1."id"
          ORDER BY t2."id" DESC
          LIMIT $1
        ) s3
      ) l4 ON true
      WHERE t1."author_id" = t0."id"
      ORDER BY t1."id" ASC
      LIMIT $2
    ) s5
  ) l6 ON true
  ORDER BY t0."id" ASC
  LIMIT $3
) s7
-- args: [2,3,101,100]
