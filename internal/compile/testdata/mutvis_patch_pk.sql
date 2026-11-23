-- request: PATCH /api/posts/1?select=id,title,users(posts(id,title))&users.posts.title=like.patched*
WITH __mut AS (
  UPDATE "public"."posts"
  SET "title" = $1
  WHERE "public"."posts"."id" = $2
  RETURNING "id", "author_id", "title"
),
__vis AS NOT MATERIALIZED (
  SELECT "id", "author_id", "title" FROM "public"."posts" WHERE ("public"."posts"."id" = $2) IS NOT TRUE
  UNION ALL SELECT __mut."id", __mut."author_id", __mut."title" FROM __mut
)
SELECT json_build_object('id', t0."id", 'title', t0."title", 'users', l5.data) AS data
FROM __mut t0
LEFT JOIN LATERAL (
  SELECT json_build_object('posts', l4.data) AS data
  FROM "public"."users" t1
  LEFT JOIN LATERAL (
    SELECT coalesce(json_agg(s3.data), '[]'::json) AS data
    FROM (
      SELECT json_build_object('id', t2."id", 'title', t2."title") AS data
      FROM __vis t2
      WHERE t2."author_id" = t1."id" AND t2."title" LIKE $3
      ORDER BY t2."id" ASC
      LIMIT $4
    ) s3
  ) l4 ON true
  WHERE t1."id" = t0."author_id"
  LIMIT 1
) l5 ON true
-- args: ["patched-probe",1,"patched%",100]
