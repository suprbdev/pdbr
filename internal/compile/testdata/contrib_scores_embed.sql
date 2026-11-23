-- request: GET /api/kb/embeddings?select=id,chunks(id,~emb_cosine_distance)&chunks.order=emb.cosine_distance&chunks.limit=2&~vec=%5B1%2C0%2C0%5D&limit=2
SELECT json_build_object('has_next', coalesce(bool_or(s4.__rn > $3), false), 'has_prev', false, 'first', (json_agg(s4.__key ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $3))->0, 'last', (json_agg(s4.__key ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $3))->-1, 'n', count(*) FILTER (WHERE s4.__rn <= $3), 'rows', coalesce(json_agg(s4.data ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $3), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'chunks', l3.data) AS data, jsonb_build_array(t0."id") AS __key, t0.__rn
  FROM (
    SELECT t0."id", row_number() OVER (ORDER BY t0."id" ASC) AS __rn
    FROM "kb"."embeddings" t0
    ORDER BY t0."id" ASC
    LIMIT 3
  ) t0
  LEFT JOIN LATERAL (
    SELECT coalesce(json_agg(s2.data), '[]'::json) AS data
    FROM (
      SELECT json_build_object('id', t1."id", '~emb_cosine_distance', (t1."emb") OPERATOR("ext".<=>) $1::"ext".vector) AS data
      FROM "kb"."chunks" t1
      WHERE t1."doc_id" = t0."id"
      ORDER BY (t1."emb") OPERATOR("ext".<=>) $2::"ext".vector ASC, t1."id" ASC
      LIMIT 2
    ) s2
  ) l3 ON true
) s4
-- args: ["[1,0,0]","[1,0,0]",2]
