-- request: GET /api/rpc/all_moods
SELECT to_json(__fn.v) AS data
FROM (SELECT "public"."all_moods"() AS v) AS __fn
-- args: []
