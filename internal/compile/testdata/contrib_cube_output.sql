-- request: GET /api/kb/shapes?select=id,box,pt
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $1), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $1))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $1))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $1), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $1), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'box', (SELECT CASE WHEN json_array_length(__cube.j) = 1 THEN __cube.j -> 0 ELSE __cube.j END FROM (SELECT ('[' || translate((t0."box")::text, '()', '[]') || ']')::json) AS __cube(j)), 'pt', (SELECT CASE WHEN json_array_length(__cube.j) = 1 THEN __cube.j -> 0 ELSE __cube.j END FROM (SELECT ('[' || translate((t0."pt")::text, '()', '[]') || ']')::json) AS __cube(j))) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "kb"."shapes" t0
  ORDER BY t0."id" ASC
  LIMIT 101
) s1
-- args: [100]
