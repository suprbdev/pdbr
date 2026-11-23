-- request: GET /api/rpc/pick_first?a=x&b=y
SELECT to_json(__fn.v) AS data
FROM (SELECT "public"."pick_first"("a" => $1::"text", "b" => $2::"text") AS v) AS __fn
-- args: ["x","y"]
