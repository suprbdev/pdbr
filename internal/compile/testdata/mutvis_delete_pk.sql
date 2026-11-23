-- request: DELETE /api/posts/5?select=id,users(posts(id))
WITH __mut AS (
  DELETE FROM "public"."posts"
  WHERE "public"."posts"."id" = $1
  RETURNING "id", "author_id"
),
__vis AS NOT MATERIALIZED (
  SELECT "id", "author_id" FROM "public"."posts" WHERE ("public"."posts"."id" = $1) IS NOT TRUE
)
SELECT json_build_object('id', t0."id", 'users', l5.data) AS data
FROM __mut t0
LEFT JOIN LATERAL (
  SELECT json_build_object('posts', l4.data) AS data
  FROM "public"."users" t1
  LEFT JOIN LATERAL (
    SELECT coalesce(json_agg(s3.data), '[]'::json) AS data
    FROM (
      SELECT json_build_object('id', t2."id") AS data
      FROM __vis t2
      WHERE t2."author_id" = t1."id"
      ORDER BY t2."id" ASC
      LIMIT 100
    ) s3
  ) l4 ON true
  WHERE t1."id" = t0."author_id"
  LIMIT 1
) l5 ON true
-- args: [5]
