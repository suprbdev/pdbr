-- request: GET /api/kb/shapes?select=id,box_chebyshev_distance,pt_l2_distance,pt_l1_distance&box=vec_chebyshev_lt.2.5&vec=%5B1%2C2%5D
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $6), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $6), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'box_chebyshev_distance', (t0."box") OPERATOR("ext".<=>) $1::"ext".cube, 'pt_l2_distance', (t0."pt") OPERATOR("ext".<->) $2::"ext".cube, 'pt_l1_distance', (t0."pt") OPERATOR("ext".<#>) $3::"ext".cube) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "kb"."shapes" t0
  WHERE ((t0."box") OPERATOR("ext".&&) "ext".cube_enlarge($4::"ext".cube, $5::float8, "ext".cube_dim($4::"ext".cube)) AND ((t0."box") OPERATOR("ext".<=>) $4::"ext".cube) < $5::float8)
  ORDER BY t0."id" ASC
  LIMIT 101
) s1
-- args: ["(1, 2)","(1, 2)","(1, 2)","(1, 2)",2.5,100]
