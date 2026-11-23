-- request: GET /api/rpc/post_array
SELECT CASE WHEN __fn.v IS NULL THEN NULL ELSE (SELECT coalesce(json_agg(CASE WHEN ((__fn.v)[e0.o])::text IS NULL THEN NULL ELSE json_build_object('id', ((__fn.v)[e0.o])."id", 'author_id', ((__fn.v)[e0.o])."author_id", 'title', ((__fn.v)[e0.o])."title", 'body', ((__fn.v)[e0.o])."body", 'published', ((__fn.v)[e0.o])."published") END ORDER BY e0.o), '[]'::json) FROM (SELECT generate_subscripts(__fn.v, 1) AS o) e0) END AS data
FROM (SELECT "public"."post_array"() AS v) AS __fn
-- args: []
