-- request: GET /api/users/1?select=id,recent_posts(n=2,id,title)
SELECT json_build_object('etag', __q.h || '.' || md5(__q.data::text), 'row', __q.data) AS data
FROM (
  SELECT md5((json_build_object('id', t0."id", 'email', t0."email", 'full_name', t0."full_name", 'mood', t0."mood", 'settings', t0."settings", 'tags', to_json(t0."tags"), 'balance', (t0."balance")::text, 'created_at', t0."created_at", 'address', CASE WHEN (t0."address")::text IS NULL THEN NULL ELSE json_build_object('street', (t0."address")."street", 'city', (t0."address")."city", 'mood', (t0."address")."mood") END, 'prev_addresses', CASE WHEN t0."prev_addresses" IS NULL THEN NULL ELSE (SELECT coalesce(json_agg(CASE WHEN ((t0."prev_addresses")[e3.o])::text IS NULL THEN NULL ELSE json_build_object('street', ((t0."prev_addresses")[e3.o])."street", 'city', ((t0."prev_addresses")[e3.o])."city", 'mood', ((t0."prev_addresses")[e3.o])."mood") END ORDER BY e3.o), '[]'::json) FROM (SELECT generate_subscripts(t0."prev_addresses", 1) AS o) e3) END, 'moods', to_json(t0."moods")))::text) AS h, json_build_object('id', t0."id", 'recent_posts', (SELECT coalesce(json_agg(s2.data), '[]'::json) FROM (
    SELECT json_build_object('id', t1."id", 'title', t1."title") AS data
    FROM "public"."users_recent_posts"("u" => ROW(t0."id", t0."email", t0."full_name", t0."mood", t0."settings", t0."tags", t0."balance", t0."created_at", t0."address", t0."prev_addresses", t0."moods")::"public"."users", "n" => $1::"int4") t1
  ) s2)) AS data
  FROM "public"."users" t0
  WHERE t0."id" = $2
  LIMIT 1
) __q
-- args: [2,1]
