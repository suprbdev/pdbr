-- request: PATCH /api/users/1?select=id,full_name,places(id,name)
WITH __mut AS (
  UPDATE "public"."users"
  SET "full_name" = $2
  WHERE "public"."users"."id" = $1
  RETURNING "id", "full_name"
),
__c_1 AS (
  UPDATE "public"."places"
  SET "owner_id" = __mut."id"
  FROM __mut
  WHERE "public"."places"."id" = $3
  RETURNING "public"."places".*
),
__c_2 AS (
  DELETE FROM "public"."places"
  USING __mut
  WHERE "public"."places"."id" = $4 AND "public"."places"."owner_id" = __mut."id"
  RETURNING "public"."places".*
),
__c_3 AS (
  UPDATE "public"."places"
  SET "name" = $6
  FROM __mut
  WHERE "public"."places"."id" = $5 AND "public"."places"."owner_id" = __mut."id"
  RETURNING "public"."places".*
),
__vis_1 AS NOT MATERIALIZED (
  SELECT "id", "owner_id", "name" FROM "public"."places" WHERE NOT EXISTS (SELECT 1 FROM __c_1 __m WHERE __m."id" = "public"."places"."id") AND NOT EXISTS (SELECT 1 FROM __c_2 __m WHERE __m."id" = "public"."places"."id") AND NOT EXISTS (SELECT 1 FROM __c_3 __m WHERE __m."id" = "public"."places"."id")
  UNION ALL SELECT "id", "owner_id", "name" FROM __c_1
  UNION ALL SELECT "id", "owner_id", "name" FROM __c_3
),
__guard AS MATERIALIZED (
  SELECT (SELECT CASE WHEN count(*) > 0 THEN true ELSE concat('pdbr guard: row of "users": matched ', count(*), ' rows')::boolean END FROM __mut)
    AND (SELECT CASE WHEN count(*) > 0 THEN true ELSE concat('pdbr guard: nested "places.connect[0]": matched ', count(*), ' rows')::boolean END FROM __c_1)
    AND (SELECT CASE WHEN count(*) > 0 THEN true ELSE concat('pdbr guard: nested "places.delete[0]": matched ', count(*), ' rows')::boolean END FROM __c_2)
    AND (SELECT CASE WHEN count(*) > 0 THEN true ELSE concat('pdbr guard: nested "places.update[0]": matched ', count(*), ' rows')::boolean END FROM __c_3) AS ok
)
SELECT __guarded.data
FROM __guard
LEFT JOIN LATERAL (
  SELECT json_build_object('id', t0."id", 'full_name', t0."full_name", 'places', l3.data) AS data
  FROM __mut t0
  LEFT JOIN LATERAL (
    SELECT coalesce(json_agg(s2.data), '[]'::json) AS data
    FROM (
      SELECT json_build_object('id', t1."id", 'name', t1."name") AS data
      FROM __vis_1 t1
      WHERE t1."owner_id" = t0."id"
      ORDER BY t1."id" ASC
      LIMIT 100
    ) s2
  ) l3 ON true
) __guarded ON true
WHERE __guard.ok
-- args: [1,"Ada",3,2,1,"Trafalgar"]
