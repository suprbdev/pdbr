-- request: GET /api/posts?select=id::text,excerpt(max_chars=5),...users(email,theme:settings->>theme,n:post_count)&title=imatch(any).{^n,^c}&order=id
SELECT json_build_object('has_next', coalesce(bool_or(s3.__rn > $5), false), 'has_prev', false, 'first', (json_agg(s3.__key ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $5))->0, 'last', (json_agg(s3.__key ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $5))->-1, 'n', count(*) FILTER (WHERE s3.__rn <= $5), 'rows', coalesce(json_agg(s3.data ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $5), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', to_json((t0."id")::"pg_catalog"."text"), 'excerpt', "public"."posts_excerpt"("p" => ROW(t0."id", t0."author_id", t0."title", t0."body", t0."published")::"public"."posts", "max_chars" => $1::"int4"), 'email', l1.v0, 'theme', l1.v1, 'n', l1.v2) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."posts" t0
  LEFT JOIN LATERAL (
    SELECT t2."email" AS v0, (t2."settings" ->> $2::text) AS v1, ("public"."users_post_count"("u" => ROW(t2."id", t2."email", t2."full_name", t2."mood", t2."settings", t2."tags", t2."balance", t2."created_at", t2."address", t2."prev_addresses", t2."moods")::"public"."users"))::text AS v2
    FROM "public"."users" t2
    WHERE t2."id" = t0."author_id"
    LIMIT 1
  ) l1 ON true
  WHERE t0."title" ~* ANY($3::"text"[])
  ORDER BY t0."id" ASC
  LIMIT $4
) s3
-- args: [5,"theme",["^n","^c"],101,100]
