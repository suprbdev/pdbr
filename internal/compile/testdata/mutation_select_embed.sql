-- request: POST /api/posts?select=id,title,users(id,email)
WITH __mut AS (
  INSERT INTO "public"."posts" ("author_id", "title")
  VALUES ($1, $2)
  RETURNING "id", "author_id", "title"
)
SELECT json_build_object('row', json_build_object('id', t0."id", 'title', t0."title", 'users', l2.data), 'key', jsonb_build_array(t0."id")) AS data
FROM __mut t0
LEFT JOIN LATERAL (
  SELECT json_build_object('id', t1."id", 'email', t1."email") AS data
  FROM "public"."users" t1
  WHERE t1."id" = t0."author_id"
  LIMIT 1
) l2 ON true
-- args: [1,"Hello"]
