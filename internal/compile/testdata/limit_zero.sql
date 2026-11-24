-- request: GET /api/users?select=id,posts(id)&posts.limit=0&limit=0&count=true
SELECT json_build_object('has_next', coalesce(bool_or(s4.__rn > $1), false), 'has_prev', false, 'first', (json_agg(s4.__key ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $1))->0, 'last', (json_agg(s4.__key ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $1))->-1, 'total', (SELECT count(*) FROM "public"."users" c5), 'n', count(*) FILTER (WHERE s4.__rn <= $1), 'rows', coalesce(json_agg(s4.data ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $1), '[]'::json)) AS data
FROM (
  SELECT NULL::json AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."users" t0
  ORDER BY t0."id" ASC
  LIMIT 1
) s4
-- args: [0]
