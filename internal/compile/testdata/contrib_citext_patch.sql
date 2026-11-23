-- request: PATCH /api/kb/authors/Ada@Example.com?select=email
WITH cur AS (
  SELECT md5(to_jsonb(__cur)::text) AS h
  FROM "kb"."authors" __cur
  WHERE __cur."email" OPERATOR("ext".=) $2::"ext".citext
),
__mut AS (
  UPDATE "kb"."authors"
  SET "name" = $1
  WHERE "kb"."authors"."email" OPERATOR("ext".=) $2::"ext".citext
    AND (SELECT h FROM cur) = $3
  RETURNING "email"
)
SELECT json_build_object('found', EXISTS (SELECT 1 FROM cur), 'row', (
  SELECT json_build_object('email', t0."email")
  FROM __mut t0
)) AS data
-- args: ["Ada","Ada@Example.com","abc"]
