-- request: GET /api/kb/docs?select=id&or=(~path_nlevel.eq.1,~path_nlevel.not.in.(2,3))&~path_nlevel=lt(any).{4,5}
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $4), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $4), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "kb"."docs" t0
  WHERE (("ext".nlevel(t0."path")) = $1 OR ("ext".nlevel(t0."path")) <> ALL($2::"int4"[])) AND ("ext".nlevel(t0."path")) < ANY($3::"int4"[])
  ORDER BY t0."id" ASC
  LIMIT 101
) s1
-- args: [1,[2,3],[4,5],100]
