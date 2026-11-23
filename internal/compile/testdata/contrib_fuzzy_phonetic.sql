-- request: GET /api/kb/docs?select=id&body=soundex.Robert&or=(body.metaphone.Knight,body.not.dmetaphone.Smith)
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $4), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $4), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "kb"."docs" t0
  WHERE "ext".soundex(t0."body") = "ext".soundex($1::text) AND (NOT ("ext".dmetaphone(t0."body") = "ext".dmetaphone($2::text)) OR "ext".metaphone(CASE WHEN octet_length((t0."body")::text) <= 255 THEN (t0."body")::text END, 255) = "ext".metaphone($3::text, 255))
  ORDER BY t0."id" ASC
  LIMIT 101
) s1
-- args: ["Robert","Smith","Knight",100]
