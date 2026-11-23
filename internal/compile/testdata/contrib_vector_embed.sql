-- request: GET /api/kb/embeddings?select=id,chunks(id,emb)&chunks.order=emb.vec_cos&chunks.limit=3&order=id&vec=%5B1%2C2%2C3%5D&limit=5
SELECT json_build_object('has_next', coalesce(bool_or(s4.__rn > $2), false), 'has_prev', false, 'first', (json_agg(s4.__key ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $2))->0, 'last', (json_agg(s4.__key ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $2))->-1, 'n', count(*) FILTER (WHERE s4.__rn <= $2), 'rows', coalesce(json_agg(s4.data ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $2), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'chunks', l3.data) AS data, jsonb_build_array(t0."id") AS __key, t0.__rn
  FROM (
    SELECT t0."id", row_number() OVER (ORDER BY t0."id" ASC) AS __rn
    FROM "kb"."embeddings" t0
    ORDER BY t0."id" ASC
    LIMIT 6
  ) t0
  LEFT JOIN LATERAL (
    SELECT coalesce(json_agg(s2.data), '[]'::json) AS data
    FROM (
      SELECT json_build_object('id', t1."id", 'emb', (t1."emb")::text::json) AS data
      FROM "kb"."chunks" t1
      WHERE t1."doc_id" = t0."id"
      ORDER BY (t1."emb") OPERATOR("ext".<=>) $1::"ext".vector ASC, t1."id" ASC
      LIMIT 3
    ) s2
  ) l3 ON true
) s4
-- args: ["[1,2,3]",5]
