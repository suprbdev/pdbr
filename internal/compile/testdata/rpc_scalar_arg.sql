-- request: GET /api/rpc/describe_mood?m=happy
SELECT to_json(__fn.v) AS data
FROM (SELECT "public"."describe_mood"("m" => $1::"public"."mood") AS v) AS __fn
-- args: ["happy"]
