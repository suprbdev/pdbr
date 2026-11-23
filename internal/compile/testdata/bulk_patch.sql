-- request: PATCH /api/users?mood=eq.sad&id=gt.1
WITH __mut AS (
  UPDATE "public"."users" m0
  SET "full_name" = $3
  WHERE m0."id" > $1 AND m0."mood" = $2
  RETURNING "id", "email", "full_name", "mood", "settings", "tags", "balance", "created_at", "address", "prev_addresses", "moods"
)
SELECT jsonb_build_object('n', count(*), 'rows', coalesce(jsonb_agg(s3.data), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', t1."id", 'email', t1."email", 'full_name', t1."full_name", 'mood', t1."mood", 'settings', t1."settings", 'tags', to_jsonb(t1."tags"), 'balance', (t1."balance")::text, 'created_at', t1."created_at", 'address', CASE WHEN (t1."address")::text IS NULL THEN NULL ELSE jsonb_build_object('street', (t1."address")."street", 'city', (t1."address")."city", 'mood', (t1."address")."mood") END, 'prev_addresses', CASE WHEN t1."prev_addresses" IS NULL THEN NULL ELSE (SELECT coalesce(jsonb_agg(CASE WHEN ((t1."prev_addresses")[e2.o])::text IS NULL THEN NULL ELSE jsonb_build_object('street', ((t1."prev_addresses")[e2.o])."street", 'city', ((t1."prev_addresses")[e2.o])."city", 'mood', ((t1."prev_addresses")[e2.o])."mood") END ORDER BY e2.o), '[]'::jsonb) FROM (SELECT generate_subscripts(t1."prev_addresses", 1) AS o) e2) END, 'moods', to_jsonb(t1."moods")) AS data
  FROM __mut t1
) s3
-- args: [1,"sad","Grace"]
