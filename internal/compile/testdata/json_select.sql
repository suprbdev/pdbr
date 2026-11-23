-- request: GET /api/users?select=id,settings->theme,settings->>theme,deep:settings->a->>b,settings->"a.b"->-1,tags->0,tags->>-1,address->>city,prev_addresses->0->>street
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $18), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $18))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $18))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $18), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $18), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", $2::text, (t0."settings" -> $1::text), $4::text, (t0."settings" ->> $3::text), 'deep', (t0."settings" -> $5::text ->> $6::text), $9::text, (t0."settings" -> $7::text -> $8::int4), 'tags', (to_jsonb(t0."tags") -> $10::int4), 'tags', (to_jsonb(t0."tags") ->> $11::int4), $13::text, (to_jsonb(t0."address") ->> $12::text), $16::text, (to_jsonb(t0."prev_addresses") -> $14::int4 ->> $15::text)) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."users" t0
  ORDER BY t0."id" ASC
  LIMIT $17
) s1
-- args: ["theme","theme","theme","theme","a","b","a.b",-1,"a.b",0,-1,"city","city",0,"street","street",101,100]
