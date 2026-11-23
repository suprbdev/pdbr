-- request: GET /api/kb/docs?select=id&attrs=hk_def.color&or=(attrs.not.hk_def.size,attrs->>size.in.(S,M))
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $5), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $5))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $5))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $5), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $5), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "kb"."docs" t0
  WHERE "ext".defined(t0."attrs", $1::text) AND (((t0."attrs") OPERATOR("ext".->) $2::text) = ANY($3::"text"[]) OR NOT ("ext".defined(t0."attrs", $4::text)))
  ORDER BY t0."id" ASC
  LIMIT 101
) s1
-- args: ["color","size",["S","M"],"size",100]
