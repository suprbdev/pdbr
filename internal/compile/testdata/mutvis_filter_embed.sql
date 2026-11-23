-- request: POST /api/posts?select=id,users(id,posts(id))&users.posts.id=gt.1&users.posts.order=id.desc&users.posts.limit=2
WITH __mut AS (
  INSERT INTO "public"."posts" ("author_id", "title")
  VALUES ($1, $2)
  RETURNING "id", "author_id"
),
__vis AS NOT MATERIALIZED (
  SELECT "id", "author_id" FROM "public"."posts"
  UNION ALL SELECT __mut."id", __mut."author_id" FROM __mut
)
SELECT json_build_object('key', jsonb_build_array(t0."id"), 'row', json_build_object('id', t0."id", 'users', l5.data)) AS data
FROM __mut t0
LEFT JOIN LATERAL (
  SELECT json_build_object('id', t1."id", 'posts', l4.data) AS data
  FROM "public"."users" t1
  LEFT JOIN LATERAL (
    SELECT coalesce(json_agg(s3.data), '[]'::json) AS data
    FROM (
      SELECT json_build_object('id', t2."id") AS data
      FROM __vis t2
      WHERE t2."author_id" = t1."id" AND t2."id" > $3
      ORDER BY t2."id" DESC
      LIMIT 2
    ) s3
  ) l4 ON true
  WHERE t1."id" = t0."author_id"
  LIMIT 1
) l5 ON true
-- args: [1,"filtered",1]
