-- request: GET /api/audit/snaps?select=id,post,history&limit=5
SELECT json_build_object('has_next', coalesce(bool_or(s2.__rn > $1), false), 'has_prev', false, 'first', (json_agg(s2.__key ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $1))->0, 'last', (json_agg(s2.__key ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $1))->-1, 'n', count(*) FILTER (WHERE s2.__rn <= $1), 'rows', coalesce(json_agg(s2.data ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $1), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'post', CASE WHEN (t0."post")::text IS NULL THEN NULL ELSE json_build_object('id', (t0."post")."id", 'author_id', (t0."post")."author_id", 'title', (t0."post")."title", 'body', (t0."post")."body", 'published', (t0."post")."published") END, 'history', CASE WHEN t0."history" IS NULL THEN NULL ELSE (SELECT coalesce(json_agg(CASE WHEN ((t0."history")[e1.o])::text IS NULL THEN NULL ELSE json_build_object('id', ((t0."history")[e1.o])."id", 'author_id', ((t0."history")[e1.o])."author_id", 'title', ((t0."history")[e1.o])."title", 'body', ((t0."history")[e1.o])."body", 'published', ((t0."history")[e1.o])."published") END ORDER BY e1.o), '[]'::json) FROM (SELECT generate_subscripts(t0."history", 1) AS o) e1) END) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "audit"."snaps" t0
  ORDER BY t0."id" ASC
  LIMIT 6
) s2
-- args: [5]
