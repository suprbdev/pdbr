-- request: GET /api/kb/docs?select=id,~body_headline,short:~body_headline(max_words=10,min_words=3,start_sel="%3Cmark%3E",stop_sel="%3C/mark%3E",fragment_delimiter="%20…%20",max_fragments=2)&body=wfts(english).fat%20cat&limit=5
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $12), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $12))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $12))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $12), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $12), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", '~body_headline', ts_headline($2::regconfig, left(t0."body", $3::int4), websearch_to_tsquery($2::regconfig, $1::text), $4::text), $9::text, ts_headline($6::regconfig, left(t0."body", $7::int4), websearch_to_tsquery($6::regconfig, $5::text), $8::text)) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "kb"."docs" t0
  WHERE to_tsvector($11::regconfig, t0."body") @@ websearch_to_tsquery($11::regconfig, $10::text)
  ORDER BY t0."id" ASC
  LIMIT 6
) s1
-- args: ["fat cat","english",65536,"","fat cat","english",65536,"MaxWords=10, MinWords=3, MaxFragments=2, StartSel=\"\u003cmark\u003e\", StopSel=\"\u003c/mark\u003e\", FragmentDelimiter=\" … \"","short","fat cat","english",5]
