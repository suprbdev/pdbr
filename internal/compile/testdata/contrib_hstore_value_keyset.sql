-- request: GET /api/kb/docs?select=id&order=attrs->color,path_nlevel.desc&limit=2&after=WyJrYi5kb2NzIiwgM10
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $6), false), 'has_prev', true, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $6), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY ((t0."attrs") OPERATOR("ext".->) $1::text) ASC, ("ext".nlevel(t0."path")) DESC NULLS LAST, t0."id" ASC) AS __rn
  FROM "kb"."docs" t0
  WHERE ((((t0."attrs") OPERATOR("ext".->) $5::text) > (SELECT ((__a."attrs") OPERATOR("ext".->) $3::text) FROM "kb"."docs" __a WHERE __a."id" = $2) OR (((t0."attrs") OPERATOR("ext".->) $5::text) IS NULL AND (SELECT ((__a."attrs") OPERATOR("ext".->) $3::text) FROM "kb"."docs" __a WHERE __a."id" = $2) IS NOT NULL)) OR (((t0."attrs") OPERATOR("ext".->) $5::text) IS NOT DISTINCT FROM (SELECT ((__a."attrs") OPERATOR("ext".->) $3::text) FROM "kb"."docs" __a WHERE __a."id" = $2) AND ((("ext".nlevel(t0."path")) < (SELECT ("ext".nlevel(__a."path")) FROM "kb"."docs" __a WHERE __a."id" = $2) OR (("ext".nlevel(t0."path")) IS NULL AND (SELECT ("ext".nlevel(__a."path")) FROM "kb"."docs" __a WHERE __a."id" = $2) IS NOT NULL)) OR (("ext".nlevel(t0."path")) IS NOT DISTINCT FROM (SELECT ("ext".nlevel(__a."path")) FROM "kb"."docs" __a WHERE __a."id" = $2) AND t0."id" > $4))))
  ORDER BY ((t0."attrs") OPERATOR("ext".->) $1::text) ASC, ("ext".nlevel(t0."path")) DESC NULLS LAST, t0."id" ASC
  LIMIT 3
) s1
-- args: ["color",3,"color",3,"color",2]
