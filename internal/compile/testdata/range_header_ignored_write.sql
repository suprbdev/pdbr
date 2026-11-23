-- request: DELETE /api/posts?id=eq.1
WITH __mut AS (
  DELETE FROM "public"."posts" m0
  WHERE m0."id" = $1
  RETURNING "id", "author_id", "title", "body", "published"
)
SELECT json_build_object('n', count(*), 'rows', coalesce(json_agg(s2.data), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t1."id", 'author_id', t1."author_id", 'title', t1."title", 'body', t1."body", 'published', t1."published") AS data
  FROM __mut t1
) s2
-- args: [1]
