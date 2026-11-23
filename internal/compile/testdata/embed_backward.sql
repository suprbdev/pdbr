-- request: GET /api/users?select=id,posts(id,title)&posts.order=title.desc&posts.limit=5&posts.offset=5&posts.title=sw.A
SELECT json_build_object('has_next', coalesce(bool_or(s4.__rn > $5), false), 'has_prev', false, 'first', (json_agg(s4.__key ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $5))->0, 'last', (json_agg(s4.__key ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $5))->-1, 'n', count(*) FILTER (WHERE s4.__rn <= $5), 'rows', coalesce(json_agg(s4.data ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $5), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'posts', l3.data) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."users" t0
  LEFT JOIN LATERAL (
    SELECT coalesce(json_agg(s2.data), '[]'::json) AS data
    FROM (
      SELECT json_build_object('id', t1."id", 'title', t1."title") AS data
      FROM "public"."posts" t1
      WHERE t1."author_id" = t0."id" AND t1."title" LIKE $1 || '%'
      ORDER BY t1."title" DESC, t1."id" ASC
      LIMIT $2
      OFFSET $3
    ) s2
  ) l3 ON true
  ORDER BY t0."id" ASC
  LIMIT $4
) s4
-- args: ["A",5,5,101,100]
