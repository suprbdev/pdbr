-- request: GET /api/users?select=id&email=like(any).{ada*,*hopper*}&email=ilike(all).{*@*,*.COM}&id=gt(any).{1,2}&mood=eq(any).{happy,ok}&settings->>theme=match(any).{^d,^l}&email=not.eq(any).{x@y.z}&id=lte(all).{10,20}
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $9), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $9))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $9))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $9), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $9), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."users" t0
  WHERE NOT (t0."email" = ANY($1::"text"[])) AND t0."email" ILIKE ALL($2::"text"[]) AND t0."email" LIKE ANY($3::"text"[]) AND t0."id" > ANY($4::"int4"[]) AND t0."id" <= ALL($5::"int4"[]) AND t0."mood" = ANY($6::text[]::"public"."mood"[]) AND (t0."settings" ->> $7::text) ~ ANY($8::"text"[])
  ORDER BY t0."id" ASC
  LIMIT 101
) s1
-- args: [["x@y.z"],["%@%","%.COM"],["ada%","%hopper%"],[1,2],[10,20],["happy","ok"],"theme",["^d","^l"],100]
