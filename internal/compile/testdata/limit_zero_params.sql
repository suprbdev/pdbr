-- request: GET /api/users?select=uid:id,mail:email,...posts(t:title,comments(id))&posts.title=like.*a*&id=gt.1&limit=0&count=true
SELECT json_build_object('has_next', coalesce(bool_or(s7.__rn > $2), false), 'has_prev', false, 'first', (json_agg(s7.__key ORDER BY s7.__rn ASC) FILTER (WHERE s7.__rn <= $2))->0, 'last', (json_agg(s7.__key ORDER BY s7.__rn ASC) FILTER (WHERE s7.__rn <= $2))->-1, 'total', (SELECT count(*) FROM "public"."users" c8 WHERE c8."id" > $3), 'n', count(*) FILTER (WHERE s7.__rn <= $2), 'rows', coalesce(json_agg(s7.data ORDER BY s7.__rn ASC) FILTER (WHERE s7.__rn <= $2), '[]'::json)) AS data
FROM (
  SELECT NULL::json AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."users" t0
  WHERE t0."id" > $1
  ORDER BY t0."id" ASC
  LIMIT 1
) s7
-- args: [1,0,1]
