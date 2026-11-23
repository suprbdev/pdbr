-- request: PATCH /api/places/1?select=id,users(id,email)
WITH __p_1 AS (
  SELECT * FROM "public"."users"
  WHERE "email" = $2
),
__mut AS (
  UPDATE "public"."places"
  SET "owner_id" = (SELECT "id" FROM __p_1)
  WHERE "public"."places"."id" = $1 AND EXISTS (SELECT 1 FROM __p_1)
  RETURNING "id", "owner_id"
),
__guard AS MATERIALIZED (
  SELECT (SELECT CASE WHEN count(*) > 0 THEN true ELSE concat('pdbr guard: row of "places": matched ', count(*), ' rows')::boolean END FROM __mut)
    AND (SELECT CASE WHEN count(*) > 0 THEN true ELSE concat('pdbr guard: nested "users.connect": matched ', count(*), ' rows')::boolean END FROM __p_1) AS ok
)
SELECT __guarded.data
FROM __guard
LEFT JOIN LATERAL (
  SELECT json_build_object('id', t0."id", 'users', l2.data) AS data
  FROM __mut t0
  LEFT JOIN LATERAL (
    SELECT json_build_object('id', t1."id", 'email', t1."email") AS data
    FROM "public"."users" t1
    WHERE t1."id" = t0."owner_id"
    LIMIT 1
  ) l2 ON true
) __guarded ON true
WHERE __guard.ok
-- args: [1,"grace@example.com"]
