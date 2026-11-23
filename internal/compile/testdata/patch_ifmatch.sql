-- request: PATCH /api/users/1
WITH cur AS (
  SELECT md5((json_build_object('id', __cur."id", 'email', __cur."email", 'full_name', __cur."full_name", 'mood', __cur."mood", 'settings', __cur."settings", 'tags', to_json(__cur."tags"), 'balance', (__cur."balance")::text, 'created_at', __cur."created_at", 'address', CASE WHEN (__cur."address")::text IS NULL THEN NULL ELSE json_build_object('street', (__cur."address")."street", 'city', (__cur."address")."city", 'mood', (__cur."address")."mood") END, 'prev_addresses', CASE WHEN __cur."prev_addresses" IS NULL THEN NULL ELSE (SELECT coalesce(json_agg(CASE WHEN ((__cur."prev_addresses")[e0.o])::text IS NULL THEN NULL ELSE json_build_object('street', ((__cur."prev_addresses")[e0.o])."street", 'city', ((__cur."prev_addresses")[e0.o])."city", 'mood', ((__cur."prev_addresses")[e0.o])."mood") END ORDER BY e0.o), '[]'::json) FROM (SELECT generate_subscripts(__cur."prev_addresses", 1) AS o) e0) END, 'moods', to_json(__cur."moods")))::text) AS h
  FROM "public"."users" __cur
  WHERE __cur."id" = $2
),
__mut AS (
  UPDATE "public"."users"
  SET "full_name" = $1
  WHERE "public"."users"."id" = $2
    AND (SELECT h FROM cur) = $3
  RETURNING "id", "email", "full_name", "mood", "settings", "tags", "balance", "created_at", "address", "prev_addresses", "moods"
)
SELECT json_build_object('found', EXISTS (SELECT 1 FROM cur), 'row', (
  SELECT json_build_object('id', t1."id", 'email', t1."email", 'full_name', t1."full_name", 'mood', t1."mood", 'settings', t1."settings", 'tags', to_json(t1."tags"), 'balance', (t1."balance")::text, 'created_at', t1."created_at", 'address', CASE WHEN (t1."address")::text IS NULL THEN NULL ELSE json_build_object('street', (t1."address")."street", 'city', (t1."address")."city", 'mood', (t1."address")."mood") END, 'prev_addresses', CASE WHEN t1."prev_addresses" IS NULL THEN NULL ELSE (SELECT coalesce(json_agg(CASE WHEN ((t1."prev_addresses")[e2.o])::text IS NULL THEN NULL ELSE json_build_object('street', ((t1."prev_addresses")[e2.o])."street", 'city', ((t1."prev_addresses")[e2.o])."city", 'mood', ((t1."prev_addresses")[e2.o])."mood") END ORDER BY e2.o), '[]'::json) FROM (SELECT generate_subscripts(t1."prev_addresses", 1) AS o) e2) END, 'moods', to_json(t1."moods"))
  FROM __mut t1
)) AS data
-- args: ["Grace",1,"5d41402abc4b2a76b9719d911017c592"]
