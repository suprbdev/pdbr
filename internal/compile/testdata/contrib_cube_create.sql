-- request: POST /api/kb/shapes?select=id,box,pt
WITH __mut AS (
  INSERT INTO "kb"."shapes" ("id", "name", "box", "pt")
  VALUES ($1, $2, $3::"ext".cube, $4)
  RETURNING "id", "box", "pt"
)
SELECT json_build_object('key', jsonb_build_array(t0."id"), 'row', json_build_object('id', t0."id", 'box', (SELECT CASE WHEN json_array_length(__cube.j) = 1 THEN __cube.j -> 0 ELSE __cube.j END FROM (SELECT ('[' || translate((t0."box")::text, '()', '[]') || ']')::json) AS __cube(j)), 'pt', (SELECT CASE WHEN json_array_length(__cube.j) = 1 THEN __cube.j -> 0 ELSE __cube.j END FROM (SELECT ('[' || translate((t0."pt")::text, '()', '[]') || ']')::json) AS __cube(j)))) AS data
FROM __mut t0
-- args: [1,"unit","(0, 0),(1, 1)","(1, 2)"]
