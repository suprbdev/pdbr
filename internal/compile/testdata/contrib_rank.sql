-- request: GET /api/kb/docs?select=id&search=wfts(english).fat%20cat&body=plfts.rat&order=search.rank.desc,body.rank_cd.desc.nullsfirst&limit=5
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $8), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $8))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $8))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $8), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $8), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY ts_rank(t0."search", websearch_to_tsquery($2::regconfig, $1::text)) DESC NULLS LAST, ts_rank_cd(to_tsvector(t0."body"), plainto_tsquery($3::text)) DESC, t0."id" ASC) AS __rn
  FROM "kb"."docs" t0
  WHERE to_tsvector(t0."body") @@ plainto_tsquery($4::text) AND (t0."search") @@ websearch_to_tsquery($6::regconfig, $5::text)
  ORDER BY ts_rank(t0."search", websearch_to_tsquery($2::regconfig, $1::text)) DESC NULLS LAST, ts_rank_cd(to_tsvector(t0."body"), plainto_tsquery($3::text)) DESC, t0."id" ASC
  LIMIT $7
) s1
-- args: ["fat cat","english","rat","rat","fat cat","english",6,5]
