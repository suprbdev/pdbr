-- request: POST /api/kb/tiles
WITH __mut AS (
  INSERT INTO "kb"."tiles" ("id", "name", "rast")
  VALUES ($1, $2, $3)
  RETURNING "id", "name"
)
SELECT json_build_object('key', jsonb_build_array(t0."id"), 'row', json_build_object('id', t0."id", 'name', t0."name")) AS data
FROM __mut t0
-- args: [1,"tile","0100000000000000000000F03F000000000000F0BF000000000000000000000000000000000000000000000000000000000000000000000000E61001000100"]
