-- request: GET /api/kb/docs?select=id,attrs->color,size:attrs->>size,attrs->>w::int&attrs->>color=eq.red&attrs->size=like.X*&order=attrs->color.desc,id&limit=5
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $12), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $12))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $12))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $12), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $12), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", $2::text, ((t0."attrs") OPERATOR("ext".->) $1::text), $4::text, ((t0."attrs") OPERATOR("ext".->) $3::text), $6::text, to_json((((t0."attrs") OPERATOR("ext".->) $5::text))::"pg_catalog"."int4")) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY ((t0."attrs") OPERATOR("ext".->) $7::text) DESC NULLS LAST, t0."id" ASC) AS __rn
  FROM "kb"."docs" t0
  WHERE ((t0."attrs") OPERATOR("ext".->) $8::text) = $9 AND ((t0."attrs") OPERATOR("ext".->) $10::text) LIKE $11
  ORDER BY ((t0."attrs") OPERATOR("ext".->) $7::text) DESC NULLS LAST, t0."id" ASC
  LIMIT 6
) s1
-- args: ["color","color","size","size","w","w","color","color","red","size","X%",5]
