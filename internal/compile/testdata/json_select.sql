-- request: GET /api/users?select=id,settings->theme,settings->>mode,deep:settings->a->>b,settings->"a.b"->-1,tags->0,last:tags->>-1,address->>city,prev_addresses->0->>street
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $20), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $20))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $20))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $20), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $20), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", $2::text, (t0."settings" -> $1::text), $4::text, (t0."settings" ->> $3::text), $7::text, (t0."settings" -> $5::text ->> $6::text), $10::text, (t0."settings" -> $8::text -> $9::int4), 'tags', (to_jsonb(t0."tags") -> $11::int4), $13::text, (to_jsonb(t0."tags") ->> $12::int4), $15::text, (to_jsonb(t0."address") ->> $14::text), $18::text, (to_jsonb(t0."prev_addresses") -> $16::int4 ->> $17::text)) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."users" t0
  ORDER BY t0."id" ASC
  LIMIT $19
) s1
-- args: ["theme","theme","mode","mode","a","b","deep","a.b",-1,"a.b",0,-1,"last","city","city",0,"street","street",101,100]
