-- request: PATCH /api/users/1?select=id,posts(id,users(full_name))
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
  RETURNING "id", "full_name"
),
__vis AS NOT MATERIALIZED (
  SELECT "id", "full_name" FROM "public"."users" WHERE ("public"."users"."id" = $2) IS NOT TRUE
  UNION ALL SELECT __mut."id", __mut."full_name" FROM __mut
)
SELECT json_build_object('found', EXISTS (SELECT 1 FROM cur), 'row', (
  SELECT json_build_object('id', t1."id", 'posts', l6.data)
  FROM __mut t1
  LEFT JOIN LATERAL (
    SELECT coalesce(json_agg(s5.data), '[]'::json) AS data
    FROM (
      SELECT json_build_object('id', t2."id", 'users', l4.data) AS data
      FROM "public"."posts" t2
      LEFT JOIN LATERAL (
        SELECT json_build_object('full_name', t3."full_name") AS data
        FROM __vis t3
        WHERE t3."id" = t2."author_id"
        LIMIT 1
      ) l4 ON true
      WHERE t2."author_id" = t1."id"
      ORDER BY t2."id" ASC
      LIMIT 100
    ) s5
  ) l6 ON true
)) AS data
-- args: ["Grace",1,"5d41402abc4b2a76b9719d911017c592"]
