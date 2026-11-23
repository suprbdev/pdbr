-- request: GET /api/users?select=id,settings->>theme&order=settings->>theme.desc.nullsfirst,settings->n
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $6), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $6), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", $2::text, (t0."settings" ->> $1::text)) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY (t0."settings" ->> $3::text) DESC, (t0."settings" -> $4::text) ASC, t0."id" ASC) AS __rn
  FROM "public"."users" t0
  ORDER BY (t0."settings" ->> $3::text) DESC, (t0."settings" -> $4::text) ASC, t0."id" ASC
  LIMIT $5
) s1
-- args: ["theme","theme","theme","n",101,100]
