-- request: GET /api/users?select=id,n:settings->n::int,settings->>theme::text,m:mood::text,b:balance::int8
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $8), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $8))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $8))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $8), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $8), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", $2::text, to_json(((t0."settings" -> $1::text))::"pg_catalog"."int4"), $4::text, to_json(((t0."settings" ->> $3::text))::"pg_catalog"."text"), $5::text, to_json((t0."mood")::"pg_catalog"."text"), $6::text, to_json((t0."balance")::"pg_catalog"."int8")) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."users" t0
  ORDER BY t0."id" ASC
  LIMIT $7
) s1
-- args: ["n","n","theme","theme","m","b",101,100]
