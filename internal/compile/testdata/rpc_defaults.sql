-- request: GET /api/rpc/greet?greeting=hi
SELECT to_json(__fn.v) AS data
FROM (SELECT "public"."greet"("greeting" => $1::"text") AS v) AS __fn
-- args: ["hi"]
