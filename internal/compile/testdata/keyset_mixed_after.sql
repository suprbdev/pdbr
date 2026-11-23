-- request: GET /api/posts?select=id,title&order=title.asc,author_id.desc&limit=2&after=WyJwdWJsaWMucG9zdHMiLDJd
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $4), false), 'has_prev', true, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $4), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'title', t0."title") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."title" ASC, t0."author_id" DESC, t0."id" ASC) AS __rn
  FROM "public"."posts" t0
  WHERE t0."title" >= (SELECT __a."title" FROM "public"."posts" __a WHERE __a."id" = $1) AND (t0."title" > (SELECT __a."title" FROM "public"."posts" __a WHERE __a."id" = $1) OR (t0."title" = (SELECT __a."title" FROM "public"."posts" __a WHERE __a."id" = $1) AND (t0."author_id" < (SELECT __a."author_id" FROM "public"."posts" __a WHERE __a."id" = $1) OR (t0."author_id" = (SELECT __a."author_id" FROM "public"."posts" __a WHERE __a."id" = $1) AND t0."id" > $2))))
  ORDER BY t0."title" ASC, t0."author_id" DESC, t0."id" ASC
  LIMIT $3
) s1
-- args: [2,2,3,2]
