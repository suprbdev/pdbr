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
),
__guard AS MATERIALIZED (
  SELECT (SELECT CASE WHEN count(*) > 0 THEN true ELSE concat('pdbr guard <token>: nested "authors.connect": matched ', count(*), ' rows')::boolean END FROM __p_1) AS ok
)
SELECT __guarded.data
FROM __guard
LEFT JOIN LATERAL (
  SELECT json_build_object('key', jsonb_build_array(t0."id"), 'row', json_build_object('id', t0."id", 'author', t0."author", 'body', t0."body")) AS data
  FROM __mut t0
) __guarded ON true
WHERE __guard.ok
-- args: ["ADA@example.com",1,"x"]
