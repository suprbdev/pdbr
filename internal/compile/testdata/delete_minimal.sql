-- request: DELETE /api/posts/2
WITH __mut AS (
  DELETE FROM "public"."posts"
  WHERE "public"."posts"."id" = $1
  RETURNING 1
)
SELECT json_build_object('n', (SELECT count(*) FROM __mut)) AS data
-- args: [2]
