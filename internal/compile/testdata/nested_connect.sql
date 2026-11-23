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
),
__guard AS MATERIALIZED (
  SELECT (SELECT CASE WHEN count(*) > 0 THEN true ELSE concat('pdbr guard: nested "users.connect": matched ', count(*), ' rows')::boolean END FROM __p_1) AS ok
)
SELECT __guarded.data
FROM __guard
LEFT JOIN LATERAL (
  SELECT json_build_object('n', (SELECT count(*) FROM __mut), 'key', (SELECT jsonb_build_array(t0."id") FROM __mut t0)) AS data
) __guarded ON true
WHERE __guard.ok
-- args: [1,"Hello",false]
