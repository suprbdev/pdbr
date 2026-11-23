-- request: PATCH /api/places/4
WITH __mut AS (
  UPDATE "public"."places"
  SET "name" = $2, "owner_id" = NULL
  WHERE "public"."places"."id" = $1
  RETURNING 1
),
__guard AS MATERIALIZED (
  SELECT (SELECT CASE WHEN count(*) > 0 THEN true ELSE concat('pdbr guard <token>: row of "places": matched ', count(*), ' rows')::boolean END FROM __mut) AS ok
)
SELECT __guarded.data
FROM __guard
LEFT JOIN LATERAL (
  SELECT json_build_object('n', (SELECT count(*) FROM __mut)) AS data
) __guarded ON true
WHERE __guard.ok
-- args: [4,"Opera House"]
