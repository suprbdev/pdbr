-- request: GET /api/users?select=id,email&order=mood.asc,email.desc&limit=2&after=WyJwdWJsaWMudXNlcnMiLDJd
SELECT json_build_object('rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4), '[]'::json), 'has_next', coalesce(bool_or(s1.__rn > $4), false), 'has_prev', true, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4))->-1) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'email', t0."email") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."mood" ASC, t0."email" DESC, t0."id" ASC) AS __rn
  FROM "public"."users" t0
  WHERE ((t0."mood" > (SELECT __a."mood" FROM "public"."users" __a WHERE __a."id" = $1) OR (t0."mood" IS NULL AND (SELECT __a."mood" FROM "public"."users" __a WHERE __a."id" = $1) IS NOT NULL)) OR (t0."mood" IS NOT DISTINCT FROM (SELECT __a."mood" FROM "public"."users" __a WHERE __a."id" = $1) AND (t0."email" < (SELECT __a."email" FROM "public"."users" __a WHERE __a."id" = $1) OR (t0."email" = (SELECT __a."email" FROM "public"."users" __a WHERE __a."id" = $1) AND t0."id" > $2))))
  ORDER BY t0."mood" ASC, t0."email" DESC, t0."id" ASC
  LIMIT $3
) s1
-- args: [2,2,3,2]
