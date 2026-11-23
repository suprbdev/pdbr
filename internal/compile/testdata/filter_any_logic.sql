-- request: GET /api/posts?select=id&or=(title.like(any).{N*,C*},id.eq(all).{1,1},title.imatch(all).{^n,engine$})
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $4), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $4), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."posts" t0
  WHERE (t0."id" = ALL($1::"int4"[]) OR t0."title" ~* ALL($2::"text"[]) OR t0."title" LIKE ANY($3::"text"[]))
  ORDER BY t0."id" ASC
  LIMIT 101
) s1
-- args: [[1,1],["^n","engine$"],["N%","C%"],100]
