-- request: GET /api/users?select=id&or=(settings->>theme.eq.dark,settings->"a.b".is.null,not.settings->>x.eq.1)
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $6), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $6), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."users" t0
  WHERE ((t0."settings" ->> $1::text) = $2 OR NOT ((t0."settings" ->> $3::text) = $4) OR (t0."settings" -> $5::text) IS NULL)
  ORDER BY t0."id" ASC
  LIMIT 101
) s1
-- args: ["theme","dark","x","1","a.b",100]
