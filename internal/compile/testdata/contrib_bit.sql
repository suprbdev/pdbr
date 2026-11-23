-- request: GET /api/kb/embeddings?select=id,code_hamming_distance,code_jaccard_distance&code=vec_hamming_lt.2&order=code.vec_hamming,id&vec=101&limit=5
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $6), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $6), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'code_hamming_distance', (t0."code") OPERATOR("ext".<~>) $1::varbit, 'code_jaccard_distance', (t0."code") OPERATOR("ext".<%>) $2::varbit) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY (t0."code") OPERATOR("ext".<~>) $3::varbit ASC, t0."id" ASC) AS __rn
  FROM "kb"."embeddings" t0
  WHERE ((t0."code") OPERATOR("ext".<~>) $4::varbit) < $5::float8
  ORDER BY (t0."code") OPERATOR("ext".<~>) $3::varbit ASC, t0."id" ASC
  LIMIT 6
) s1
-- args: ["101","101","101","101",2,5]
