-- request: POST /api/posts?select=id,users(email,posts(id,title))
WITH __mut AS (
  INSERT INTO "public"."posts" ("author_id", "title")
  VALUES ($1, $2)
  RETURNING "id", "author_id", "title"
),
__vis AS NOT MATERIALIZED (
  SELECT "id", "author_id", "title" FROM "public"."posts"
  UNION ALL SELECT __mut."id", __mut."author_id", __mut."title" FROM __mut
)
SELECT json_build_object('key', jsonb_build_array(t0."id"), 'row', json_build_object('id', t0."id", 'users', l5.data)) AS data
FROM __mut t0
LEFT JOIN LATERAL (
  SELECT json_build_object('email', t1."email", 'posts', l4.data) AS data
  FROM "public"."users" t1
  LEFT JOIN LATERAL (
    SELECT coalesce(json_agg(s3.data), '[]'::json) AS data
    FROM (
      SELECT json_build_object('id', t2."id", 'title', t2."title") AS data
      FROM __vis t2
      WHERE t2."author_id" = t1."id"
      ORDER BY t2."id" ASC
      LIMIT $3
    ) s3
  ) l4 ON true
  WHERE t1."id" = t0."author_id"
  LIMIT 1
) l5 ON true
-- args: [1,"vis-probe",100]
