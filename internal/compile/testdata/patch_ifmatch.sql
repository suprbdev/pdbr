-- request: PATCH /api/users/1
WITH cur AS (
  SELECT md5(to_jsonb(__cur)::text) AS h
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
  SELECT json_build_object('id', t0."id", 'email', t0."email", 'full_name', t0."full_name", 'mood', t0."mood", 'settings', t0."settings", 'tags', to_json(t0."tags"), 'balance', (t0."balance")::text, 'created_at', t0."created_at", 'address', CASE WHEN (t0."address")::text IS NULL THEN NULL ELSE json_build_object('street', (t0."address")."street", 'city', (t0."address")."city", 'mood', (t0."address")."mood") END, 'prev_addresses', CASE WHEN t0."prev_addresses" IS NULL THEN NULL ELSE (SELECT coalesce(json_agg(CASE WHEN ((t0."prev_addresses")[e1.o])::text IS NULL THEN NULL ELSE json_build_object('street', ((t0."prev_addresses")[e1.o])."street", 'city', ((t0."prev_addresses")[e1.o])."city", 'mood', ((t0."prev_addresses")[e1.o])."mood") END ORDER BY e1.o), '[]'::json) FROM (SELECT generate_subscripts(t0."prev_addresses", 1) AS o) e1) END, 'moods', to_json(t0."moods"))
  FROM __mut t0
)) AS data
-- args: ["Grace",1,"5d41402abc4b2a76b9719d911017c592"]
