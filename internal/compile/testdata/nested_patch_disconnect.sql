-- request: PATCH /api/users/1?select=id,places(id)
WITH __mut AS (
  SELECT "id" FROM "public"."users"
  WHERE "public"."users"."id" = $1
  FOR UPDATE
),
__c_1 AS (
  UPDATE "public"."places"
  SET "owner_id" = NULL
  FROM __mut
  WHERE "public"."places"."id" = $2 AND "public"."places"."owner_id" = __mut."id"
  RETURNING "public"."places".*
),
__vis_1 AS NOT MATERIALIZED (
  SELECT "id", "owner_id" FROM "public"."places" WHERE NOT EXISTS (SELECT 1 FROM __c_1 __m WHERE __m."id" = "public"."places"."id")
  UNION ALL SELECT "id", "owner_id" FROM __c_1
),
__guard AS MATERIALIZED (
  SELECT (SELECT CASE WHEN count(*) > 0 THEN true ELSE concat('pdbr guard <token>: row of "users": matched ', count(*), ' rows')::boolean END FROM __mut) AS ok
)
SELECT __guarded.data
FROM __guard
LEFT JOIN LATERAL (
  SELECT json_build_object('id', t0."id", 'places', l3.data) AS data
  FROM __mut t0
  LEFT JOIN LATERAL (
    SELECT coalesce(json_agg(s2.data), '[]'::json) AS data
    FROM (
      SELECT json_build_object('id', t1."id") AS data
      FROM __vis_1 t1
      WHERE t1."owner_id" = t0."id"
      ORDER BY t1."id" ASC
      LIMIT 100
    ) s2
  ) l3 ON true
) __guarded ON true
WHERE __guard.ok
-- args: [1,1]
