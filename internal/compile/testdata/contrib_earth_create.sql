-- request: POST /api/kb/sites?select=id,loc
WITH __mut AS (
  INSERT INTO "kb"."sites" ("id", "name", "loc", "ll")
  VALUES ($1, $2, "ext".ll_to_earth($3::float8, $4::float8), $5)
  RETURNING "id", "loc"
)
SELECT json_build_object('key', jsonb_build_array(t0."id"), 'row', json_build_object('id', t0."id", 'loc', (SELECT CASE WHEN __earth.e IS NULL THEN NULL ELSE json_build_object('lat', "ext".latitude(__earth.e), 'lon', "ext".longitude(__earth.e)) END FROM (SELECT t0."loc" AS e) AS __earth))) AS data
FROM __mut t0
-- args: [1,"Trafalgar Square",51.508,-0.128,"(-0.128,51.508)"]
