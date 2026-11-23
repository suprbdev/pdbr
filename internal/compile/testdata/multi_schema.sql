-- request: GET /api/audit/log?action=eq.login&order=id.desc&limit=5
SELECT json_build_object('rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $3), '[]'::json), 'has_next', coalesce(bool_or(s1.__rn > $3), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $3))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $3))->-1) AS data
FROM (
  SELECT json_build_object('id', (t0."id")::text, 'action', t0."action", 'at', t0."at") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" DESC) AS __rn
  FROM "audit"."log" t0
  WHERE t0."action" = $1
  ORDER BY t0."id" DESC
  LIMIT $2
) s1
-- args: ["login",6,5]
