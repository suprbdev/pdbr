-- request: POST /api/kb/embeddings
WITH __mut AS (
  INSERT INTO "kb"."embeddings" ("id", "title", "emb", "half", "sparse")
  VALUES ($1, $2, $3::"ext".vector, $4::"ext".halfvec, $5::"ext".sparsevec)
  RETURNING "id", "title", "emb", "half", "sparse"
)
SELECT json_build_object('key', jsonb_build_array(t0."id"), 'row', json_build_object('id', t0."id", 'title', t0."title", 'emb', (t0."emb")::text::json, 'half', (t0."half")::text::json, 'sparse', t0."sparse")) AS data
FROM __mut t0
-- args: [7,"t","[0.1,0.2,0.3]","[1,2,3]","{2:1.5}/3"]
