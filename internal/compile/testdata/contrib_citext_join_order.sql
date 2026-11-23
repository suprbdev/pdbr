-- request: GET /api/kb/notes?select=id,authors(name)&order=authors(email).desc&after=WyJrYi5ub3RlcyIsN10&limit=2
SELECT jsonb_build_object('rows', coalesce(jsonb_agg(s3.data ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $4), '[]'::jsonb), 'has_next', coalesce(bool_or(s3.__rn > $4), false), 'has_prev', true, 'first', (jsonb_agg(s3.__key ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $4))->0, 'last', (jsonb_agg(s3.__key ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $4))->-1) AS data
FROM (
  SELECT jsonb_build_object('id', t0."id", 'authors', l2.data) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY (SELECT __ro."email" FROM "kb"."authors" __ro WHERE __ro."email" OPERATOR("ext".=) t0."author") DESC NULLS LAST, t0."id" ASC) AS __rn
  FROM "kb"."notes" t0
  LEFT JOIN LATERAL (
    SELECT jsonb_build_object('name', t1."name") AS data
    FROM "kb"."authors" t1
    WHERE t1."email" OPERATOR("ext".=) t0."author"
    LIMIT 1
  ) l2 ON true
  WHERE (((SELECT __ro."email" FROM "kb"."authors" __ro WHERE __ro."email" OPERATOR("ext".=) t0."author") OPERATOR("ext".<) (SELECT (SELECT __ro."email" FROM "kb"."authors" __ro WHERE __ro."email" OPERATOR("ext".=) __a."author") FROM "kb"."notes" __a WHERE __a."id" = $1) OR ((SELECT __ro."email" FROM "kb"."authors" __ro WHERE __ro."email" OPERATOR("ext".=) t0."author") IS NULL AND (SELECT (SELECT __ro."email" FROM "kb"."authors" __ro WHERE __ro."email" OPERATOR("ext".=) __a."author") FROM "kb"."notes" __a WHERE __a."id" = $1) IS NOT NULL)) OR (((SELECT __ro."email" FROM "kb"."authors" __ro WHERE __ro."email" OPERATOR("ext".=) t0."author") OPERATOR("ext".=) (SELECT (SELECT __ro."email" FROM "kb"."authors" __ro WHERE __ro."email" OPERATOR("ext".=) __a."author") FROM "kb"."notes" __a WHERE __a."id" = $1) OR ((SELECT __ro."email" FROM "kb"."authors" __ro WHERE __ro."email" OPERATOR("ext".=) t0."author") IS NULL AND (SELECT (SELECT __ro."email" FROM "kb"."authors" __ro WHERE __ro."email" OPERATOR("ext".=) __a."author") FROM "kb"."notes" __a WHERE __a."id" = $1) IS NULL)) AND t0."id" > $2))
  ORDER BY (SELECT __ro."email" FROM "kb"."authors" __ro WHERE __ro."email" OPERATOR("ext".=) t0."author") DESC NULLS LAST, t0."id" ASC
  LIMIT $3
) s3
-- args: [7,7,3,2]
