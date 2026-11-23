-- request: GET /api/users?select=id&email=isdistinct.ada@example.com&id=not.isdistinct.3&tags=isdistinct.{a,b}
SELECT json_build_object('rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $5), '[]'::json), 'has_next', coalesce(bool_or(s1.__rn > $5), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $5))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $5))->-1) AS data
FROM (
  SELECT json_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."users" t0
  WHERE t0."email" IS DISTINCT FROM $1 AND NOT (t0."id" IS DISTINCT FROM $2) AND t0."tags" IS DISTINCT FROM $3::"text"[]
  ORDER BY t0."id" ASC
  LIMIT $4
) s1
-- args: ["ada@example.com",3,["a","b"],101,100]
