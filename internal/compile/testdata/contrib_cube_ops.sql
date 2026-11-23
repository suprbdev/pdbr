-- request: GET /api/kb/shapes?select=id&box=cs.%5B1%2C2%5D&or=(box.cd."(0,0),(10,10)",box.not.ov."[[0,0],[1,1]]",box.is.null)
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $4), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $4), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "kb"."shapes" t0
  WHERE (t0."box") OPERATOR("ext".@>) $1::"ext".cube AND ((t0."box") OPERATOR("ext".<@) $2::"ext".cube OR t0."box" IS NULL OR NOT ((t0."box") OPERATOR("ext".&&) $3::"ext".cube))
  ORDER BY t0."id" ASC
  LIMIT 101
) s1
-- args: ["(1, 2)","(0,0),(10,10)","(0, 0),(1, 1)",100]
