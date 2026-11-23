-- request: PUT /api/users?on_conflict=email&select=email,posts(users(email,full_name))
WITH __mut AS (
  INSERT INTO "public"."users" ("email", "full_name")
  VALUES ($1, $2), ($3, $4)
  ON CONFLICT ("email") DO UPDATE
  SET "full_name" = EXCLUDED."full_name"
  RETURNING "id", "email", "full_name"
),
__vis AS NOT MATERIALIZED (
  SELECT "id", "email", "full_name" FROM "public"."users" WHERE NOT EXISTS (SELECT 1 FROM __mut WHERE __mut."email" = "public"."users"."email")
  UNION ALL SELECT __mut."id", __mut."email", __mut."full_name" FROM __mut
)
SELECT coalesce(json_agg(s6.data), '[]'::json) AS data
FROM (
  SELECT json_build_object('email', t0."email", 'posts', l5.data) AS data
  FROM __mut t0
  LEFT JOIN LATERAL (
    SELECT coalesce(json_agg(s4.data), '[]'::json) AS data
    FROM (
      SELECT json_build_object('users', l3.data) AS data
      FROM "public"."posts" t1
      LEFT JOIN LATERAL (
        SELECT json_build_object('email', t2."email", 'full_name', t2."full_name") AS data
        FROM __vis t2
        WHERE t2."id" = t1."author_id"
        LIMIT 1
      ) l3 ON true
      WHERE t1."author_id" = t0."id"
      ORDER BY t1."id" ASC
      LIMIT 100
    ) s4
  ) l5 ON true
) s6
-- args: ["a@b.c","Ada","d@e.f","Dee"]
