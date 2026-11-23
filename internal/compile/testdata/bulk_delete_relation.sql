-- request: DELETE /api/posts?users.email=eq.ada@example.com&select=id
WITH __mut AS (
  DELETE FROM "public"."posts" m0
  WHERE EXISTS (SELECT 1 FROM "public"."users" r1 WHERE r1."id" = m0."author_id" AND (r1."email" = $1))
  RETURNING "id"
)
SELECT json_build_object('n', count(*), 'rows', coalesce(json_agg(s3.data), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t2."id") AS data
  FROM __mut t2
) s3
-- args: ["ada@example.com"]
