-- request: GET /api/kb/authors?select=email&meta=eq.a%3D%3E1&order=meta.desc&after=WyJrYi5hdXRob3JzIiwiQWRhQEV4YW1wbGUuY29tIl0&limit=2
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $5), false), 'has_prev', true, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $5))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $5))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $5), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $5), '[]'::json)) AS data
FROM (
  SELECT json_build_object('email', t0."email") AS data, jsonb_build_array(t0."email") AS __key, row_number() OVER (ORDER BY t0."meta" DESC NULLS LAST, t0."email" ASC) AS __rn
  FROM "kb"."authors" t0
  WHERE ((t0."meta" OPERATOR("ext".#<#) (SELECT __a."meta" FROM "kb"."authors" __a WHERE __a."email" OPERATOR("ext".=) $1::"ext".citext) OR (t0."meta" IS NULL AND (SELECT __a."meta" FROM "kb"."authors" __a WHERE __a."email" OPERATOR("ext".=) $1::"ext".citext) IS NOT NULL)) OR ((t0."meta" OPERATOR("ext".=) (SELECT __a."meta" FROM "kb"."authors" __a WHERE __a."email" OPERATOR("ext".=) $1::"ext".citext) OR (t0."meta" IS NULL AND (SELECT __a."meta" FROM "kb"."authors" __a WHERE __a."email" OPERATOR("ext".=) $1::"ext".citext) IS NULL)) AND t0."email" OPERATOR("ext".>) $2)) AND t0."meta" OPERATOR("ext".=) $3::"ext"."hstore"
  ORDER BY t0."meta" DESC NULLS LAST, t0."email" ASC
  LIMIT $4
) s1
-- args: ["Ada@Example.com","Ada@Example.com","a=\u003e1",3,2]
