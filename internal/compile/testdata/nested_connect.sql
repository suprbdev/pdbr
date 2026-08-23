-- request: POST /api/posts
WITH __p_1 AS (
  SELECT * FROM "public"."users"
  WHERE "id" = $1
),
__mut AS (
  INSERT INTO "public"."posts" ("title", "published", "author_id")
  SELECT $2, $3, __p_1."id"
  FROM __p_1
  RETURNING "id"
)
SELECT jsonb_build_object('n', (SELECT count(*) FROM __mut), 'key', (SELECT jsonb_build_array(t0."id") FROM __mut t0)) AS data
-- args: [1,"Hello",false]
