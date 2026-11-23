-- request: POST /api/kb/notes
WITH __p_1 AS (
  SELECT * FROM "kb"."authors"
  WHERE "email" OPERATOR("ext".=) $1::"ext".citext
),
__mut AS (
  INSERT INTO "kb"."notes" ("id", "body", "author")
  SELECT $2, $3, __p_1."email"
  FROM __p_1
  RETURNING "id", "author", "body"
)
SELECT jsonb_build_object('row', jsonb_build_object('id', t0."id", 'author', t0."author", 'body', t0."body"), 'key', jsonb_build_array(t0."id")) AS data
FROM __mut t0
-- args: ["ADA@example.com",1,"x"]
