-- request: GET /api/kb/authors?select=email,notes(id)&order=notes(author).asc,email&after=WyJrYi5hdXRob3JzIiwiQWRhQEV4YW1wbGUuY29tIl0&limit=2
SELECT json_build_object('rows', coalesce(json_agg(s4.data ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $5), '[]'::json), 'has_next', coalesce(bool_or(s4.__rn > $5), false), 'has_prev', true, 'first', (json_agg(s4.__key ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $5))->0, 'last', (json_agg(s4.__key ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $5))->-1) AS data
FROM (
  SELECT json_build_object('email', t0."email", 'notes', l3.data) AS data, jsonb_build_array(t0."email") AS __key, row_number() OVER (ORDER BY (SELECT "ext".min(__ro."author") FROM "kb"."notes" __ro WHERE __ro."author" OPERATOR("ext".=) t0."email") ASC, t0."email" ASC) AS __rn
  FROM "kb"."authors" t0
  LEFT JOIN LATERAL (
    SELECT coalesce(json_agg(s2.data), '[]'::json) AS data
    FROM (
      SELECT json_build_object('id', t1."id") AS data
      FROM "kb"."notes" t1
      WHERE t1."author" OPERATOR("ext".=) t0."email"
      ORDER BY t1."id" ASC
      LIMIT $1
    ) s2
  ) l3 ON true
  WHERE (((SELECT "ext".min(__ro."author") FROM "kb"."notes" __ro WHERE __ro."author" OPERATOR("ext".=) t0."email") OPERATOR("ext".>) (SELECT (SELECT "ext".min(__ro."author") FROM "kb"."notes" __ro WHERE __ro."author" OPERATOR("ext".=) __a."email") FROM "kb"."authors" __a WHERE __a."email" OPERATOR("ext".=) $2::"ext".citext) OR ((SELECT "ext".min(__ro."author") FROM "kb"."notes" __ro WHERE __ro."author" OPERATOR("ext".=) t0."email") IS NULL AND (SELECT (SELECT "ext".min(__ro."author") FROM "kb"."notes" __ro WHERE __ro."author" OPERATOR("ext".=) __a."email") FROM "kb"."authors" __a WHERE __a."email" OPERATOR("ext".=) $2::"ext".citext) IS NOT NULL)) OR (((SELECT "ext".min(__ro."author") FROM "kb"."notes" __ro WHERE __ro."author" OPERATOR("ext".=) t0."email") OPERATOR("ext".=) (SELECT (SELECT "ext".min(__ro."author") FROM "kb"."notes" __ro WHERE __ro."author" OPERATOR("ext".=) __a."email") FROM "kb"."authors" __a WHERE __a."email" OPERATOR("ext".=) $2::"ext".citext) OR ((SELECT "ext".min(__ro."author") FROM "kb"."notes" __ro WHERE __ro."author" OPERATOR("ext".=) t0."email") IS NULL AND (SELECT (SELECT "ext".min(__ro."author") FROM "kb"."notes" __ro WHERE __ro."author" OPERATOR("ext".=) __a."email") FROM "kb"."authors" __a WHERE __a."email" OPERATOR("ext".=) $2::"ext".citext) IS NULL)) AND t0."email" OPERATOR("ext".>) $3))
  ORDER BY (SELECT "ext".min(__ro."author") FROM "kb"."notes" __ro WHERE __ro."author" OPERATOR("ext".=) t0."email") ASC, t0."email" ASC
  LIMIT $4
) s4
-- args: [100,"Ada@Example.com","Ada@Example.com",3,2]
