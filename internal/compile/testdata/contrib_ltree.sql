-- request: GET /api/kb/docs?select=id,path,paths&path=cs.top.science&path=descendant.top&path=not.ancestor_any.{a.b,c}&path=descendant_any.{top}&path=lquery.*.astro*.*&path=ltxtquery.astro*%20%26%20!moon&path=gt.top.a&paths=lquery_any.{*.x,*.y}&paths=descendant.top&paths=cs.{a.b}
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $12), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $12))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $12))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $12), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $12), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'path', t0."path", 'paths', to_json(t0."paths")) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "kb"."docs" t0
  WHERE NOT ((t0."path") OPERATOR("ext".@>) $1::"ext".ltree[]) AND (t0."path") OPERATOR("ext".@>) $2::"ext".ltree AND (t0."path") OPERATOR("ext".<@) $3::"ext".ltree AND (t0."path") OPERATOR("ext".<@) $4::"ext".ltree[] AND t0."path" OPERATOR("ext".>) $5::"ext".ltree AND (t0."path") OPERATOR("ext".~) $6::"ext".lquery AND (t0."path") OPERATOR("ext".@) $7::"ext".ltxtquery AND t0."paths" @> $8::text[]::"ext"."ltree"[] AND (t0."paths") OPERATOR("ext".<@) $9::"ext".ltree AND (t0."paths") OPERATOR("ext".?) $10::"ext".lquery[]
  ORDER BY t0."id" ASC
  LIMIT $11
) s1
-- args: [["a.b","c"],"top.science","top",["top"],"top.a","*.astro*.*","astro* \u0026 !moon",["a.b"],"top",["*.x","*.y"],101,100]
