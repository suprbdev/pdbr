-- request: GET /api/kb/docs?select=id&search=fts(english).fat%20%26%20cat:*&body=wfts(pg_catalog.english).%22fat%20cat%22%20-rat&or=(search.plfts.fat%20cat,search.not.phfts(simple).fat%20cat,body.fts.rat)
SELECT jsonb_build_object('rows', coalesce(jsonb_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $10), '[]'::jsonb), 'has_next', coalesce(bool_or(s1.__rn > $10), false), 'has_prev', false, 'first', (jsonb_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $10))->0, 'last', (jsonb_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $10))->-1) AS data
FROM (
  SELECT jsonb_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "kb"."docs" t0
  WHERE to_tsvector($2::regconfig, t0."body") @@ websearch_to_tsquery($2::regconfig, $1::text) AND (t0."search") @@ to_tsquery($4::regconfig, $3::text) AND (to_tsvector(t0."body") @@ to_tsquery($5::text) OR NOT ((t0."search") @@ phraseto_tsquery($7::regconfig, $6::text)) OR (t0."search") @@ plainto_tsquery($8::text))
  ORDER BY t0."id" ASC
  LIMIT $9
) s1
-- args: ["\"fat cat\" -rat","pg_catalog.english","fat \u0026 cat:*","english","rat","fat cat","simple","fat cat",101,100]
