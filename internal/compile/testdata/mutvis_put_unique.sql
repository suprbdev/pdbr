-- request: PUT /api/users/by/email/a%40b.c?select=id,posts(users(full_name))
WITH __mut AS (
  INSERT INTO "public"."users" ("email", "full_name")
  VALUES ($1, $2)
  ON CONFLICT ("email") DO UPDATE
  SET "full_name" = EXCLUDED."full_name"
  RETURNING "id", "full_name", (xmax = 0) AS __inserted
),
__vis AS NOT MATERIALIZED (
  SELECT "id", "full_name" FROM "public"."users" WHERE ("public"."users"."email" = $1) IS NOT TRUE
  UNION ALL SELECT __mut."id", __mut."full_name" FROM __mut
)
SELECT json_build_object('inserted', t0.__inserted, 'key', jsonb_build_array(t0."id"), 'row', json_build_object('id', t0."id", 'posts', l5.data)) AS data
FROM __mut t0
LEFT JOIN LATERAL (
  SELECT coalesce(json_agg(s4.data), '[]'::json) AS data
  FROM (
    SELECT json_build_object('users', l3.data) AS data
    FROM "public"."posts" t1
    LEFT JOIN LATERAL (
      SELECT json_build_object('full_name', t2."full_name") AS data
      FROM __vis t2
      WHERE t2."id" = t1."author_id"
      LIMIT 1
    ) l3 ON true
    WHERE t1."author_id" = t0."id"
    ORDER BY t1."id" ASC
    LIMIT $3
  ) s4
) l5 ON true
-- args: ["a@b.c","Ada",100]
