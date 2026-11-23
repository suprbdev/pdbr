-- request: GET /api/kb/embeddings?select=id,emb_cosine_distance&emb=vec_cos_lt.0.25&or=(emb.vec_l2_lt.2,emb.not.vec_ip_lt.-1)&order=emb.vec_cos&vec=%5B1%2C0.5%2C-2%5D&limit=5
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $9), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $9))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $9))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $9), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $9), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'emb_cosine_distance', (t0."emb") OPERATOR("ext".<=>) $1::"ext".vector) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY (t0."emb") OPERATOR("ext".<=>) $2::"ext".vector ASC, t0."id" ASC) AS __rn
  FROM "kb"."embeddings" t0
  WHERE ((t0."emb") OPERATOR("ext".<=>) $3::"ext".vector) < $4::float8 AND (NOT (((t0."emb") OPERATOR("ext".<#>) $5::"ext".vector) < $6::float8) OR ((t0."emb") OPERATOR("ext".<->) $7::"ext".vector) < $8::float8)
  ORDER BY (t0."emb") OPERATOR("ext".<=>) $2::"ext".vector ASC, t0."id" ASC
  LIMIT 6
) s1
-- args: ["[1,0.5,-2]","[1,0.5,-2]","[1,0.5,-2]",0.25,"[1,0.5,-2]",-1,"[1,0.5,-2]",2,5]
