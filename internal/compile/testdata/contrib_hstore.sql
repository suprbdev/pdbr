-- request: GET /api/kb/docs?select=id,attrs&attrs=hk.color&and=(attrs.hk_all.{color,size},attrs.hk_any.{"a,b",c})&attrs=cs.%7B%22color%22%3A%22red%22%2C%22size%22%3Anull%7D&attrs=not.cd.color%3D%3Ered&attrs=neq.a%3D%3E1
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $7), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $7))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $7))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $7), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $7), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'attrs', t0."attrs") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "kb"."docs" t0
  WHERE ((t0."attrs") OPERATOR("ext".?&) $1::text[] AND (t0."attrs") OPERATOR("ext".?|) $2::text[]) AND NOT ((t0."attrs") OPERATOR("ext".<@) $3::"ext"."hstore") AND (t0."attrs") OPERATOR("ext".@>) "ext".hstore($4::text[]) AND (t0."attrs") OPERATOR("ext".?) $5::text AND t0."attrs" OPERATOR("ext".<>) $6::"ext"."hstore"
  ORDER BY t0."id" ASC
  LIMIT 101
) s1
-- args: [["color","size"],["a,b","c"],"color=\u003ered",["color","red","size",null],"color","a=\u003e1",100]
