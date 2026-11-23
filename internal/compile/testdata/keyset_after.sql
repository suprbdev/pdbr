-- request: GET /api/posts?order=title.asc&limit=2&count=true&after=WyJwdWJsaWMucG9zdHMiLDJd
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $3), false), 'has_prev', true, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $3))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $3))->-1, 'total', (SELECT count(*) FROM "public"."posts" c2), 'n', count(*) FILTER (WHERE s1.__rn <= $3), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $3), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'author_id', t0."author_id", 'title', t0."title", 'body', t0."body", 'published', t0."published") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."title" ASC, t0."id" ASC) AS __rn
  FROM "public"."posts" t0
  WHERE (t0."title", t0."id") > ((SELECT __a."title" FROM "public"."posts" __a WHERE __a."id" = $1), $2)
  ORDER BY t0."title" ASC, t0."id" ASC
  LIMIT 3
) s1
-- args: [2,2,2]
