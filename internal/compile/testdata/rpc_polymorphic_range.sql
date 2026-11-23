-- request: GET /api/rpc/range_lower?r=%5B3%2C5%29
SELECT to_json(__fn.v) AS data
FROM (SELECT "public"."range_lower"("r" => $1::"int4range") AS v) AS __fn
-- args: ["[3,5)"]
