-- request: GET /api/kb/docs?select=id,net&net=cs.10.1.2.3&net=cd.10.0.0.0/8&net=cs_strict.10.1.0.0/16&net=not.cd_strict.192.168.0.0/16&net=ov.10.0.0.0/8&net=eq.10.1.2.3
SELECT json_build_object('rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $8), '[]'::json), 'has_next', coalesce(bool_or(s1.__rn > $8), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $8))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $8))->-1) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'net', t0."net") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "kb"."docs" t0
  WHERE (t0."net") <<= $1::inet AND NOT ((t0."net") << $2::inet) AND (t0."net") >>= $3::inet AND (t0."net") >> $4::inet AND t0."net" = $5 AND (t0."net") && $6::inet
  ORDER BY t0."id" ASC
  LIMIT $7
) s1
-- args: ["10.0.0.0/8","192.168.0.0/16","10.1.2.3","10.1.0.0/16","10.1.2.3","10.0.0.0/8",101,100]
