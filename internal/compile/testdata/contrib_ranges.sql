-- request: GET /api/kb/docs?select=id,pages,valid,slots&pages=ov.[1,5)&pages=cs.3&pages=cd.%7B%22start%22%3A0%2C%22end%22%3Anull%7D&valid=sl.[2020-01-01,2020-02-01)&pages=sr.(,0]&pages=nxr.(,100]&pages=nxl.[0,)&valid=not.adj.empty&slots=ov.[1,3)&slots=cs.2
SELECT json_build_object('rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $28), '[]'::json), 'has_next', coalesce(bool_or(s1.__rn > $28), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $28))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $28))->-1) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'pages', t0."pages", 'valid', t0."valid", 'slots', t0."slots") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "kb"."docs" t0
  WHERE (t0."pages") <@ int4range($1::int4, $2::int4, $3::text) AND (t0."pages") @> $4::int4 AND (t0."pages") &> int4range($5::int4, $6::int4, $7::text) AND (t0."pages") &< int4range($8::int4, $9::int4, $10::text) AND (t0."pages") && int4range($11::int4, $12::int4, $13::text) AND (t0."pages") >> int4range($14::int4, $15::int4, $16::text) AND (t0."slots") @> $17::int4 AND (t0."slots") && int4range($18::int4, $19::int4, $20::text) AND NOT ((t0."valid") -|- daterange($21::date, $22::date, $23::text)) AND (t0."valid") << daterange($24::date, $25::date, $26::text)
  ORDER BY t0."id" ASC
  LIMIT $27
) s1
-- args: ["0",null,"[)","3","0",null,"[)",null,"100","(]","1","5","[)",null,"0","(]","2","1","3","[)","2000-01-01","2000-01-01","[)","2020-01-01","2020-02-01","[)",101,100]
