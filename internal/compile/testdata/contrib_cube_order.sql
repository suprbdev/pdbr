-- request: GET /api/kb/shapes?select=id,box&order=box.vec_l2,id.desc&vec=(1%2C2)&limit=5
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $2), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $2))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $2))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $2), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $2), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'box', (SELECT CASE WHEN json_array_length(__cube.j) = 1 THEN __cube.j -> 0 ELSE __cube.j END FROM (SELECT ('[' || translate((t0."box")::text, '()', '[]') || ']')::json) AS __cube(j))) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY (t0."box") OPERATOR("ext".<->) $1::"ext".cube ASC, t0."id" DESC) AS __rn
  FROM "kb"."shapes" t0
  ORDER BY (t0."box") OPERATOR("ext".<->) $1::"ext".cube ASC, t0."id" DESC
  LIMIT 6
) s1
-- args: ["(1,2)",5]
