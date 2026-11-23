-- request: PATCH /api/kb/notes?author=eq.ADA@example.com
WITH __mut AS (
  UPDATE "kb"."notes" m0
  SET "body" = $2
  WHERE m0."author" OPERATOR("ext".=) $1::"ext".citext
  RETURNING "id", "author", "body"
)
SELECT jsonb_build_object('n', count(*), 'rows', coalesce(jsonb_agg(s2.data), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', t1."id", 'author', t1."author", 'body', t1."body") AS data
  FROM __mut t1
) s2
-- args: ["ADA@example.com","y"]
