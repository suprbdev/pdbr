-- request: GET /api/users?select=id&email=match.^a.*@&mood=eq.happy&email=not.imatch.EXAMPLE$
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $5), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $5))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $5))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $5), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $5), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."users" t0
  WHERE NOT (t0."email" ~* $1) AND t0."email" ~ $2 AND t0."mood" = $3
  ORDER BY t0."id" ASC
  LIMIT $4
) s1
-- args: ["EXAMPLE$","^a.*@","happy",101,100]
