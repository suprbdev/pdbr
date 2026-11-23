-- request: GET /api/users/1?select=id,recent_posts(n=2,id,title)
SELECT json_build_object('etag', md5(to_jsonb(t0)::text), 'row', json_build_object('id', t0."id", 'recent_posts', (SELECT coalesce(json_agg(s2.data), '[]'::json) FROM (
  SELECT json_build_object('id', t1."id", 'title', t1."title") AS data
  FROM "public"."users_recent_posts"("u" => ROW(t0."id", t0."email", t0."full_name", t0."mood", t0."settings", t0."tags", t0."balance", t0."created_at", t0."address", t0."prev_addresses", t0."moods")::"public"."users", "n" => $1::"int4") t1
) s2))) AS data
FROM "public"."users" t0
WHERE t0."id" = $2
LIMIT 1
-- args: [2,1]
