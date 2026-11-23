-- request: GET /api/kb/docs?select=id,~title_trgm_similarity,~title_trgm_word_similarity,~title_trgm_strict_word_similarity&title=trgm_sim.postgres&~trgm=postgres
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $5), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $5))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $5))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $5), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $5), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", '~title_trgm_similarity', "ext".similarity(t0."title", $1::text), '~title_trgm_word_similarity', "ext".word_similarity($2::text, t0."title"), '~title_trgm_strict_word_similarity', "ext".strict_word_similarity($3::text, t0."title")) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "kb"."docs" t0
  WHERE (t0."title") OPERATOR("ext".%) $4::text
  ORDER BY t0."id" ASC
  LIMIT 101
) s1
-- args: ["postgres","postgres","postgres","postgres",100]
