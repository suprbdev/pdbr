-- request: GET /api/kb/embeddings?select=id&order=emb.vec_ip,half.vec_cos.desc&vec=%5B1%2C0%2C0%5D&limit=5
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $3), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $3))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $3))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $3), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $3), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY (t0."emb") OPERATOR("ext".<#>) $1::"ext".vector ASC, (t0."half") OPERATOR("ext".<=>) $2::"ext".halfvec DESC NULLS LAST, t0."id" ASC) AS __rn
  FROM "kb"."embeddings" t0
  ORDER BY (t0."emb") OPERATOR("ext".<#>) $1::"ext".vector ASC, (t0."half") OPERATOR("ext".<=>) $2::"ext".halfvec DESC NULLS LAST, t0."id" ASC
  LIMIT 6
) s1
-- args: ["[1,0,0]","[1,0,0]",5]
