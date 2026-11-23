-- request: PATCH /api/places/1?select=id,location_srid
WITH __mut AS (
  UPDATE "public"."places"
  SET "location" = ST_SetSRID(ST_GeomFromEWKT($1), 4326)
  WHERE "public"."places"."id" = $2
  RETURNING "id", "location"
)
SELECT jsonb_build_object('id', t0."id", 'location_srid', ST_SRID(t0."location")) AS data
FROM __mut t0
-- args: ["POINT(-0.1281 51.508)",1]
