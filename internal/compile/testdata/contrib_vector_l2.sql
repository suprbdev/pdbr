-- request: GET /api/kb/embeddings?select=id,title,emb,half,sparse&order=emb.l2_distance&~vec=%5B0.1%2C0.2%2C0.3%5D&limit=5
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $2), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $2))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $2))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $2), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $2), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'title', t0."title", 'emb', (t0."emb")::text::json, 'half', (t0."half")::text::json, 'sparse', t0."sparse") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY (t0."emb") OPERATOR("ext".<->) $1::"ext".vector ASC, t0."id" ASC) AS __rn
  FROM "kb"."embeddings" t0
  ORDER BY (t0."emb") OPERATOR("ext".<->) $1::"ext".vector ASC, t0."id" ASC
  LIMIT 6
) s1
-- args: ["[0.1,0.2,0.3]",5]
