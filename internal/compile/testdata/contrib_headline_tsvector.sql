-- request: GET /api/kb/articles?select=id,search_headline(document=title),m:raw_headline(highlight_all=true),docs(id,title,search_headline(document=body))&search=fts(simple).fat%26cat&raw=plfts.rat&docs.search=phfts(english).fat%20cat&limit=3
SELECT json_build_object('has_next', coalesce(bool_or(s3.__rn > $15), false), 'has_prev', false, 'first', (json_agg(s3.__key ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $15))->0, 'last', (json_agg(s3.__key ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $15))->-1, 'n', count(*) FILTER (WHERE s3.__rn <= $15), 'rows', coalesce(json_agg(s3.data ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $15), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'search_headline', ts_headline($2::regconfig, t0."title", to_tsquery($2::regconfig, $1::text), $3::text), $6::text, ts_headline(t0."raw", plainto_tsquery($4::text), $5::text), 'docs', l2.data) AS data, jsonb_build_array(t0."id") AS __key, t0.__rn
  FROM (
    SELECT t0."id", t0."title", t0."raw", t0."author_id", row_number() OVER (ORDER BY t0."id" ASC) AS __rn
    FROM "kb"."articles" t0
    WHERE to_tsvector(t0."raw") @@ plainto_tsquery($12::text) AND (t0."search") @@ to_tsquery($14::regconfig, $13::text)
    ORDER BY t0."id" ASC
    LIMIT 4
  ) t0
  LEFT JOIN LATERAL (
    SELECT json_build_object('id', t1."id", 'title', t1."title", 'search_headline', ts_headline($8::regconfig, t1."body", phraseto_tsquery($8::regconfig, $7::text), $9::text)) AS data
    FROM "kb"."docs" t1
    WHERE t1."id" = t0."author_id" AND (t1."search") @@ phraseto_tsquery($11::regconfig, $10::text)
    LIMIT 1
  ) l2 ON true
) s3
-- args: ["fat\u0026cat","simple","","rat","HighlightAll=true","m","fat cat","english","","fat cat","english","rat","fat\u0026cat","simple",3]
