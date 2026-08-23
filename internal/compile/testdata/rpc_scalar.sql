-- request: GET /api/rpc/best_mood
SELECT to_jsonb(__fn.v) AS data
FROM (SELECT "public"."best_mood"() AS v) AS __fn
-- args: []
