-- request: POST /api/posts?select=id
WITH __mut AS (
  INSERT INTO "public"."posts" ("author_id", "title")
  VALUES ($1, $2)
  ON CONFLICT ("id") DO NOTHING
  RETURNING "id"
)
SELECT json_build_object('row', json_build_object('id', t0."id"), 'key', jsonb_build_array(t0."id")) AS data
FROM __mut t0
-- args: [1,"t"]
