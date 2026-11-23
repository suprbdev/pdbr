-- request: GET /api/rpc/post_tab?limit=5
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $1), false), 'has_prev', false, 'n', count(*) FILTER (WHERE s1.__rn <= $1), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $1), '[]'::json)) AS data
FROM (
  SELECT json_build_object('n', t0."n", 'p', CASE WHEN (t0."p")::text IS NULL THEN NULL ELSE json_build_object('id', (t0."p")."id", 'author_id', (t0."p")."author_id", 'title', (t0."p")."title", 'body', (t0."p")."body", 'published', (t0."p")."published") END) AS data, row_number() OVER () AS __rn
  FROM "public"."post_tab"() t0
  LIMIT 6
) s1
-- args: [5]
