-- request: PATCH /api/posts?author_id=eq.1
WITH __mut AS (
  UPDATE "public"."posts" m0
  SET "published" = $2
  WHERE m0."author_id" = $1
  RETURNING 1
)
SELECT json_build_object('n', (SELECT count(*) FROM __mut)) AS data
-- args: [1,true]
