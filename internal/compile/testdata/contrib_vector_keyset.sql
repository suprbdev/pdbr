-- request: GET /api/kb/embeddings?select=id&order=emb.cosine_distance&~vec=%5B1%2C2%2C3%5D&limit=2&after=WyJrYi5lbWJlZGRpbmdzIiwgM10
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $6), false), 'has_prev', true, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $6), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY (t0."emb") OPERATOR("ext".<=>) $1::"ext".vector ASC, t0."id" ASC) AS __rn
  FROM "kb"."embeddings" t0
  WHERE (((t0."emb") OPERATOR("ext".<=>) $5::"ext".vector > (SELECT (__a."emb") OPERATOR("ext".<=>) $3::"ext".vector FROM "kb"."embeddings" __a WHERE __a."id" = $2) OR ((t0."emb") OPERATOR("ext".<=>) $5::"ext".vector IS NULL AND (SELECT (__a."emb") OPERATOR("ext".<=>) $3::"ext".vector FROM "kb"."embeddings" __a WHERE __a."id" = $2) IS NOT NULL)) OR ((t0."emb") OPERATOR("ext".<=>) $5::"ext".vector IS NOT DISTINCT FROM (SELECT (__a."emb") OPERATOR("ext".<=>) $3::"ext".vector FROM "kb"."embeddings" __a WHERE __a."id" = $2) AND t0."id" > $4))
  ORDER BY (t0."emb") OPERATOR("ext".<=>) $1::"ext".vector ASC, t0."id" ASC
  LIMIT 3
) s1
-- args: ["[1,2,3]",3,"[1,2,3]",3,"[1,2,3]",2]
