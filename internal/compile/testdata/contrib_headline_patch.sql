-- request: PATCH /api/kb/docs?body=fts.cat&select=id,body_headline
WITH __mut AS (
  UPDATE "kb"."docs" m0
  SET "title" = $2
  WHERE to_tsvector(m0."body") @@ to_tsquery($1::text)
  RETURNING "id", "body"
)
SELECT json_build_object('n', count(*), 'rows', coalesce(json_agg(s2.data), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t1."id", 'body_headline', ts_headline(left(t1."body", $4::int4), to_tsquery($3::text), $5::text)) AS data
  FROM __mut t1
) s2
-- args: ["cat","x","cat",65536,""]
