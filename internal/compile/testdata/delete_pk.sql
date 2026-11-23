-- request: DELETE /api/posts/2
WITH __mut AS (
  DELETE FROM "public"."posts"
  WHERE "public"."posts"."id" = $1
  RETURNING "id", "author_id", "title", "body", "published"
)
SELECT json_build_object('id', t0."id", 'author_id', t0."author_id", 'title', t0."title", 'body', t0."body", 'published', t0."published") AS data
FROM __mut t0
-- args: [2]
