-- request: PUT /api/users?on_conflict=email
WITH __mut AS (
  INSERT INTO "public"."users" ("email", "full_name", "mood")
  VALUES ($1, $2, DEFAULT), ($3, DEFAULT, $4)
  ON CONFLICT ("email") DO UPDATE
  SET "full_name" = EXCLUDED."full_name", "mood" = EXCLUDED."mood"
  RETURNING "id", "email", "full_name", "mood", "settings", "tags", "balance", "created_at", "address", "prev_addresses", "moods"
)
SELECT coalesce(jsonb_agg(s2.data), '[]'::jsonb) AS data
FROM (
  SELECT jsonb_build_object('id', t0."id", 'email', t0."email", 'full_name', t0."full_name", 'mood', t0."mood", 'settings', t0."settings", 'tags', to_jsonb(t0."tags"), 'balance', (t0."balance")::text, 'created_at', t0."created_at", 'address', CASE WHEN (t0."address")::text IS NULL THEN NULL ELSE jsonb_build_object('street', (t0."address")."street", 'city', (t0."address")."city", 'mood', (t0."address")."mood") END, 'prev_addresses', CASE WHEN t0."prev_addresses" IS NULL THEN NULL ELSE (SELECT coalesce(jsonb_agg(CASE WHEN ((t0."prev_addresses")[e1.o])::text IS NULL THEN NULL ELSE jsonb_build_object('street', ((t0."prev_addresses")[e1.o])."street", 'city', ((t0."prev_addresses")[e1.o])."city", 'mood', ((t0."prev_addresses")[e1.o])."mood") END ORDER BY e1.o), '[]'::jsonb) FROM (SELECT generate_subscripts(t0."prev_addresses", 1) AS o) e1) END, 'moods', to_jsonb(t0."moods")) AS data
  FROM __mut t0
) s2
-- args: ["a@b.c","Ada","d@e.f","ok"]
