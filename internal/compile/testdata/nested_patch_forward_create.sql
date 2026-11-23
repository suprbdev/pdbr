-- request: PATCH /api/places/2?select=id,users(id,email)
WITH __p_1 AS (
  INSERT INTO "public"."users" ("email")
  VALUES ($2)
  RETURNING *
),
__mut AS (
  UPDATE "public"."places"
  SET "owner_id" = (SELECT "id" FROM __p_1)
  WHERE "public"."places"."id" = $1 AND EXISTS (SELECT 1 FROM __p_1)
  RETURNING "id", "owner_id"
),
__vis_1 AS NOT MATERIALIZED (
  SELECT "id", "email" FROM "public"."users"
  UNION ALL SELECT "id", "email" FROM __p_1
),
__guard AS MATERIALIZED (
  SELECT (SELECT CASE WHEN count(*) > 0 THEN true ELSE concat('pdbr guard <token>: row of "places": matched ', count(*), ' rows')::boolean END FROM __mut) AS ok
)
SELECT __guarded.data
FROM __guard
LEFT JOIN LATERAL (
  SELECT json_build_object('id', t0."id", 'users', l2.data) AS data
  FROM __mut t0
  LEFT JOIN LATERAL (
    SELECT json_build_object('id', t1."id", 'email', t1."email") AS data
    FROM __vis_1 t1
    WHERE t1."id" = t0."owner_id"
    LIMIT 1
  ) l2 ON true
) __guarded ON true
WHERE __guard.ok
-- args: [2,"golden-owner@example.com"]
