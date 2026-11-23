-- request: GET /api/kb/authors?select=email&order=email.desc,meta.asc&limit=2&after=WyJrYi5hdXRob3JzIiwiQWRhQEV4YW1wbGUuY29tIl0
SELECT json_build_object('rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4), '[]'::json), 'has_next', coalesce(bool_or(s1.__rn > $4), false), 'has_prev', true, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4))->-1) AS data
FROM (
  SELECT json_build_object('email', t0."email") AS data, jsonb_build_array(t0."email") AS __key, row_number() OVER (ORDER BY t0."email" DESC, t0."meta" ASC) AS __rn
  FROM "kb"."authors" t0
  WHERE t0."email" OPERATOR("ext".<=) $1 AND (t0."email" OPERATOR("ext".<) $1 OR (t0."email" OPERATOR("ext".=) $1 AND (t0."meta" OPERATOR("ext".#>#) (SELECT __a."meta" FROM "kb"."authors" __a WHERE __a."email" OPERATOR("ext".=) $2::"ext".citext) OR (t0."meta" IS NULL AND (SELECT __a."meta" FROM "kb"."authors" __a WHERE __a."email" OPERATOR("ext".=) $2::"ext".citext) IS NOT NULL))))
  ORDER BY t0."email" DESC, t0."meta" ASC
  LIMIT $3
) s1
-- args: ["Ada@Example.com","Ada@Example.com",3,2]
