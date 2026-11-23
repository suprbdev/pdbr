-- request: DELETE /api/kb/authors/Ada@Example.com
WITH __mut AS (
  DELETE FROM "kb"."authors"
  WHERE "kb"."authors"."email" OPERATOR("ext".=) $1::"ext".citext
  RETURNING "email", "name", "meta"
)
SELECT json_build_object('email', t0."email", 'name', t0."name", 'meta', t0."meta") AS data
FROM __mut t0
-- args: ["Ada@Example.com"]
