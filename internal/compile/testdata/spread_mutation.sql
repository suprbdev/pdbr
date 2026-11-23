-- request: POST /api/posts?select=id,...users(author:email)
WITH __mut AS (
  INSERT INTO "public"."posts" ("author_id", "title")
  VALUES ($1, $2)
  RETURNING "id", "author_id"
)
SELECT json_build_object('key', jsonb_build_array(t0."id"), 'row', json_build_object('id', t0."id", $3::text, l1.v0)) AS data
FROM __mut t0
LEFT JOIN LATERAL (
  SELECT t2."email" AS v0
  FROM "public"."users" t2
  WHERE t2."id" = t0."author_id"
  LIMIT 1
) l1 ON true
-- args: [1,"Spread","author"]
