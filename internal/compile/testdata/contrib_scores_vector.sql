-- request: GET /api/kb/embeddings?select=id,emb_cosine_distance,emb_l2_distance,emb_negative_inner_product,sparse_l1_distance&order=emb.vec_cos&vec=%5B1%2C0.5%2C-2%5D&limit=3
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $6), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $6), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'emb_cosine_distance', (t0."emb") OPERATOR("ext".<=>) $1::"ext".vector, 'emb_l2_distance', (t0."emb") OPERATOR("ext".<->) $2::"ext".vector, 'emb_negative_inner_product', (t0."emb") OPERATOR("ext".<#>) $3::"ext".vector, 'sparse_l1_distance', (t0."sparse") OPERATOR("ext".<+>) $4::"ext".sparsevec) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY (t0."emb") OPERATOR("ext".<=>) $5::"ext".vector ASC, t0."id" ASC) AS __rn
  FROM "kb"."embeddings" t0
  ORDER BY (t0."emb") OPERATOR("ext".<=>) $5::"ext".vector ASC, t0."id" ASC
  LIMIT 4
) s1
-- args: ["[1,0.5,-2]","[1,0.5,-2]","[1,0.5,-2]","{1:1,2:0.5,3:-2}/3","[1,0.5,-2]",3]
