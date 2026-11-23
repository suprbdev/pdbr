-- request: GET /api/posts?id=gt.1&id=lte.10&title=like.*Engine*&title=not.ilike.*draft*&author_id=in.(1,2)&author_id=neq.3&title=sw.Not&title=ew.ine&title=not.is.null
SELECT json_build_object('rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $10), '[]'::json), 'has_next', coalesce(bool_or(s1.__rn > $10), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $10))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $10))->-1) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'author_id', t0."author_id", 'title', t0."title", 'body', t0."body", 'published', t0."published") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."posts" t0
  WHERE t0."author_id" = ANY($1::"int4"[]) AND t0."author_id" <> $2 AND t0."id" > $3 AND t0."id" <= $4 AND t0."title" LIKE '%' || $5 AND NOT (t0."title" ILIKE $6) AND t0."title" IS NOT NULL AND t0."title" LIKE $7 AND t0."title" LIKE $8 || '%'
  ORDER BY t0."id" ASC
  LIMIT $9
) s1
-- args: [[1,2],3,1,10,"ine","%draft%","%Engine%","Not",101,100]
