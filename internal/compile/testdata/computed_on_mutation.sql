-- request: POST /api/users?select=id,email,post_count,tag_words
WITH __mut AS (
  INSERT INTO "public"."users" ("email", "tags")
  VALUES ($1, $2::"text"[])
  RETURNING "id", "email", "full_name", "mood", "settings", "tags", "balance", "created_at", "address", "prev_addresses", "moods"
)
SELECT json_build_object('key', jsonb_build_array(t0."id"), 'row', json_build_object('id', t0."id", 'email', t0."email", 'post_count', ("public"."users_post_count"("u" => ROW(t0."id", t0."email", t0."full_name", t0."mood", t0."settings", t0."tags", t0."balance", t0."created_at", t0."address", t0."prev_addresses", t0."moods")::"public"."users"))::text, 'tag_words', (SELECT coalesce(json_agg(v1.v), '[]'::json) FROM "public"."users_tag_words"("u" => ROW(t0."id", t0."email", t0."full_name", t0."mood", t0."settings", t0."tags", t0."balance", t0."created_at", t0."address", t0."prev_addresses", t0."moods")::"public"."users") v1(v)))) AS data
FROM __mut t0
-- args: ["new@example.com",["x","y"]]
