-- request: PATCH /api/posts/1?select=id,users(id,email)&users.posts.title=eq.x
WITH __mut AS (
  UPDATE "public"."posts"
  SET "title" = $1
  WHERE "public"."posts"."id" = $2
  RETURNING "id", "author_id", "title"
),
__vis AS NOT MATERIALIZED (
  SELECT "author_id", "title" FROM "public"."posts" WHERE ("public"."posts"."id" = $2) IS NOT TRUE
  UNION ALL SELECT __mut."author_id", __mut."title" FROM __mut
)
SELECT json_build_object('id', t0."id", 'users', l3.data) AS data
FROM __mut t0
LEFT JOIN LATERAL (
  SELECT json_build_object('id', t1."id", 'email', t1."email") AS data
  FROM "public"."users" t1
  WHERE t1."id" = t0."author_id" AND EXISTS (SELECT 1 FROM __vis r2 WHERE r2."author_id" = t1."id" AND (r2."title" = $3))
  LIMIT 1
) l3 ON true
-- args: ["x",1,"x"]
