-- request: GET /api/kb/docs?select=id,~body_levenshtein_distance&body=levenshtein_distance_lt.3&order=body.levenshtein_distance,id&~levenshtein=postgres&limit=5
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $5), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $5))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $5))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $5), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $5), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", '~body_levenshtein_distance', CASE WHEN char_length((t0."body")::text) <= 255 THEN "ext".levenshtein((t0."body")::text, $1::text) END) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY CASE WHEN char_length((t0."body")::text) <= 255 THEN "ext".levenshtein((t0."body")::text, $2::text) END ASC, t0."id" ASC) AS __rn
  FROM "kb"."docs" t0
  WHERE CASE WHEN char_length((t0."body")::text) <= 255 THEN "ext".levenshtein_less_equal((t0."body")::text, $3::text, $4::int4) END < $4::int4
  ORDER BY CASE WHEN char_length((t0."body")::text) <= 255 THEN "ext".levenshtein((t0."body")::text, $2::text) END ASC, t0."id" ASC
  LIMIT 6
) s1
-- args: ["postgres","postgres","postgres",3,5]
