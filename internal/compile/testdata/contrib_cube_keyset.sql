-- request: GET /api/kb/shapes?select=id&order=box.l1_distance&~vec=%5B%5B0%2C0%5D%2C%5B1%2C1%5D%5D&limit=2&after=WyJrYi5zaGFwZXMiLCAzXQ
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $6), false), 'has_prev', true, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $6), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY (t0."box") OPERATOR("ext".<#>) $1::"ext".cube ASC, t0."id" ASC) AS __rn
  FROM "kb"."shapes" t0
  WHERE (((t0."box") OPERATOR("ext".<#>) $5::"ext".cube > (SELECT (__a."box") OPERATOR("ext".<#>) $3::"ext".cube FROM "kb"."shapes" __a WHERE __a."id" = $2) OR ((t0."box") OPERATOR("ext".<#>) $5::"ext".cube IS NULL AND (SELECT (__a."box") OPERATOR("ext".<#>) $3::"ext".cube FROM "kb"."shapes" __a WHERE __a."id" = $2) IS NOT NULL)) OR ((t0."box") OPERATOR("ext".<#>) $5::"ext".cube IS NOT DISTINCT FROM (SELECT (__a."box") OPERATOR("ext".<#>) $3::"ext".cube FROM "kb"."shapes" __a WHERE __a."id" = $2) AND t0."id" > $4))
  ORDER BY (t0."box") OPERATOR("ext".<#>) $1::"ext".cube ASC, t0."id" ASC
  LIMIT 3
) s1
-- args: ["(0, 0),(1, 1)",3,"(0, 0),(1, 1)",3,"(0, 0),(1, 1)",2]
