-- request: GET /api/kb/embeddings?select=id&order=emb.l1_distance&~vec=[1,2,3]&emb=not.is.null&limit=5
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $2), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $2))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $2))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $2), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $2), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY (t0."emb") OPERATOR("ext".<+>) $1::"ext".vector ASC, t0."id" ASC) AS __rn
  FROM "kb"."embeddings" t0
  WHERE t0."emb" IS NOT NULL
  ORDER BY (t0."emb") OPERATOR("ext".<+>) $1::"ext".vector ASC, t0."id" ASC
  LIMIT 6
) s1
-- args: ["[1,2,3]",5]
