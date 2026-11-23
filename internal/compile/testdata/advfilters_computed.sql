-- request: GET /api/users?post_count=gt.1&post_count=not.in.(5,6)&select=id,post_count&order=post_count.desc,id&limit=2
SELECT jsonb_build_object('rows', coalesce(jsonb_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4), '[]'::jsonb), 'has_next', coalesce(bool_or(s1.__rn > $4), false), 'has_prev', false, 'first', (jsonb_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4))->0, 'last', (jsonb_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4))->-1) AS data
FROM (
  SELECT jsonb_build_object('id', t0."id", 'post_count', ("public"."users_post_count"("u" => ROW(t0."id", t0."email", t0."full_name", t0."mood", t0."settings", t0."tags", t0."balance", t0."created_at", t0."address", t0."prev_addresses", t0."moods")::"public"."users"))::text) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY "public"."users_post_count"("u" => ROW(t0."id", t0."email", t0."full_name", t0."mood", t0."settings", t0."tags", t0."balance", t0."created_at", t0."address", t0."prev_addresses", t0."moods")::"public"."users") DESC NULLS LAST, t0."id" ASC) AS __rn
  FROM "public"."users" t0
  WHERE "public"."users_post_count"("u" => ROW(t0."id", t0."email", t0."full_name", t0."mood", t0."settings", t0."tags", t0."balance", t0."created_at", t0."address", t0."prev_addresses", t0."moods")::"public"."users") > $1 AND "public"."users_post_count"("u" => ROW(t0."id", t0."email", t0."full_name", t0."mood", t0."settings", t0."tags", t0."balance", t0."created_at", t0."address", t0."prev_addresses", t0."moods")::"public"."users") <> ALL($2::"int8"[])
  ORDER BY "public"."users_post_count"("u" => ROW(t0."id", t0."email", t0."full_name", t0."mood", t0."settings", t0."tags", t0."balance", t0."created_at", t0."address", t0."prev_addresses", t0."moods")::"public"."users") DESC NULLS LAST, t0."id" ASC
  LIMIT $3
) s1
-- args: [1,[5,6],3,2]
