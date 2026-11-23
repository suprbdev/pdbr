-- request: GET /api/rpc/best_mood
SELECT to_json(__fn.v) AS data
FROM (SELECT "public"."best_mood"() AS v) AS __fn
-- args: []
