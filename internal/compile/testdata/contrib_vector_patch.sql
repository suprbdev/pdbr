-- request: PATCH /api/kb/embeddings?id=eq.7
WITH __mut AS (
  UPDATE "kb"."embeddings" m0
  SET "emb" = $2::"ext".vector, "sparse" = $3::"ext".sparsevec
  WHERE m0."id" = $1
  RETURNING "id", "title", "emb", "half", "sparse", "code"
)
SELECT json_build_object('n', count(*), 'rows', coalesce(json_agg(s2.data), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t1."id", 'title', t1."title", 'emb', (t1."emb")::text::json, 'half', (t1."half")::text::json, 'sparse', t1."sparse", 'code', t1."code") AS data
  FROM __mut t1
) s2
-- args: [7,null,"{1:2}/3"]
