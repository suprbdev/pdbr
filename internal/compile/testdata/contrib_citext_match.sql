-- request: GET /api/kb/docs?select=id&email=imatch.^a&email=match(any).{^b,^c}
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $4), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $4), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "kb"."docs" t0
  WHERE t0."email" OPERATOR("ext".~*) $1::"ext".citext AND t0."email" OPERATOR("ext".~) ANY($2::text[]::"ext"."citext"[])
  ORDER BY t0."id" ASC
  LIMIT $3
) s1
-- args: ["^a",["^b","^c"],101,100]
