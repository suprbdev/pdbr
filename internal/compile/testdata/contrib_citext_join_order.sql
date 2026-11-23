-- request: GET /api/kb/notes?select=id,authors(name)&order=authors(email).desc&after=WyJrYi5ub3RlcyIsN10&limit=2
SELECT json_build_object('has_next', coalesce(bool_or(s3.__rn > $3), false), 'has_prev', true, 'first', (json_agg(s3.__key ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $3))->0, 'last', (json_agg(s3.__key ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $3))->-1, 'n', count(*) FILTER (WHERE s3.__rn <= $3), 'rows', coalesce(json_agg(s3.data ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $3), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'authors', l2.data) AS data, jsonb_build_array(t0."id") AS __key, t0.__rn
  FROM (
    SELECT t0."id", t0."author", row_number() OVER (ORDER BY (SELECT __ro."email" FROM "kb"."authors" __ro WHERE __ro."email" OPERATOR("ext".=) t0."author") DESC NULLS LAST, t0."id" ASC) AS __rn
    FROM "kb"."notes" t0
    WHERE (((SELECT __ro."email" FROM "kb"."authors" __ro WHERE __ro."email" OPERATOR("ext".=) t0."author") OPERATOR("ext".<) (SELECT (SELECT __ro."email" FROM "kb"."authors" __ro WHERE __ro."email" OPERATOR("ext".=) __a."author") FROM "kb"."notes" __a WHERE __a."id" = $1) OR ((SELECT __ro."email" FROM "kb"."authors" __ro WHERE __ro."email" OPERATOR("ext".=) t0."author") IS NULL AND (SELECT (SELECT __ro."email" FROM "kb"."authors" __ro WHERE __ro."email" OPERATOR("ext".=) __a."author") FROM "kb"."notes" __a WHERE __a."id" = $1) IS NOT NULL)) OR (((SELECT __ro."email" FROM "kb"."authors" __ro WHERE __ro."email" OPERATOR("ext".=) t0."author") OPERATOR("ext".=) (SELECT (SELECT __ro."email" FROM "kb"."authors" __ro WHERE __ro."email" OPERATOR("ext".=) __a."author") FROM "kb"."notes" __a WHERE __a."id" = $1) OR ((SELECT __ro."email" FROM "kb"."authors" __ro WHERE __ro."email" OPERATOR("ext".=) t0."author") IS NULL AND (SELECT (SELECT __ro."email" FROM "kb"."authors" __ro WHERE __ro."email" OPERATOR("ext".=) __a."author") FROM "kb"."notes" __a WHERE __a."id" = $1) IS NULL)) AND t0."id" > $2))
    ORDER BY (SELECT __ro."email" FROM "kb"."authors" __ro WHERE __ro."email" OPERATOR("ext".=) t0."author") DESC NULLS LAST, t0."id" ASC
    LIMIT 3
  ) t0
  LEFT JOIN LATERAL (
    SELECT json_build_object('name', t1."name") AS data
    FROM "kb"."authors" t1
    WHERE t1."email" OPERATOR("ext".=) t0."author"
    LIMIT 1
  ) l2 ON true
) s3
-- args: [7,7,2]
