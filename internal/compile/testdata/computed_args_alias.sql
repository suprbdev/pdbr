-- request: GET /api/posts?select=id,short:excerpt(max_chars="10")
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $3), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $3))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $3))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $3), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $3), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", $2::text, "public"."posts_excerpt"("p" => ROW(t0."id", t0."author_id", t0."title", t0."body", t0."published")::"public"."posts", "max_chars" => $1::"int4")) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."posts" t0
  ORDER BY t0."id" ASC
  LIMIT 101
) s1
-- args: [10,"short",100]
