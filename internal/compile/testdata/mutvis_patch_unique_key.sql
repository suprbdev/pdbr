-- request: PATCH /api/users/by/email/a@b.c?select=id,email,posts(users(email))
WITH __mut AS (
  UPDATE "public"."users"
  SET "email" = $1
  WHERE "public"."users"."email" = $2
  RETURNING "id", "email"
),
__vis AS NOT MATERIALIZED (
  SELECT "id", "email" FROM "public"."users" WHERE ("public"."users"."email" = $2) IS NOT TRUE
  UNION ALL SELECT __mut."id", __mut."email" FROM __mut
)
SELECT json_build_object('id', t0."id", 'email', t0."email", 'posts', l5.data) AS data
FROM __mut t0
LEFT JOIN LATERAL (
  SELECT coalesce(json_agg(s4.data), '[]'::json) AS data
  FROM (
    SELECT json_build_object('users', l3.data) AS data
    FROM "public"."posts" t1
    LEFT JOIN LATERAL (
      SELECT json_build_object('email', t2."email") AS data
      FROM __vis t2
      WHERE t2."id" = t1."author_id"
      LIMIT 1
    ) l3 ON true
    WHERE t1."author_id" = t0."id"
    ORDER BY t1."id" ASC
    LIMIT 100
  ) s4
) l5 ON true
-- args: ["new@b.c","a@b.c"]
