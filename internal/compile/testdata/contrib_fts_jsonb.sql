-- request: GET /api/kb/articles?select=id&raw=wfts(english).fat%20cat&or=(raw.not.fts.rat,id.eq.1)&order=raw.rank.desc,raw.rank_cd&limit=5
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $9), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $9))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $9))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $9), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $9), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY ts_rank(to_tsvector($2::regconfig, t0."raw"), websearch_to_tsquery($2::regconfig, $1::text)) DESC NULLS LAST, ts_rank_cd(to_tsvector($4::regconfig, t0."raw"), websearch_to_tsquery($4::regconfig, $3::text)) ASC, t0."id" ASC) AS __rn
  FROM "kb"."articles" t0
  WHERE to_tsvector($6::regconfig, t0."raw") @@ websearch_to_tsquery($6::regconfig, $5::text) AND (t0."id" = $7 OR NOT (to_tsvector(t0."raw") @@ to_tsquery($8::text)))
  ORDER BY ts_rank(to_tsvector($2::regconfig, t0."raw"), websearch_to_tsquery($2::regconfig, $1::text)) DESC NULLS LAST, ts_rank_cd(to_tsvector($4::regconfig, t0."raw"), websearch_to_tsquery($4::regconfig, $3::text)) ASC, t0."id" ASC
  LIMIT 6
) s1
-- args: ["fat cat","english","fat cat","english","fat cat","english",1,"rat",5]
