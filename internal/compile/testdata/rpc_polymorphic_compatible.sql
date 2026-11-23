-- request: POST /api/rpc/larger_of
SELECT to_json((__fn.v)::text) AS data
FROM (SELECT "public"."larger_of"("a" => $1::"numeric", "b" => $2::"numeric") AS v) AS __fn
-- args: ["1","2.5"]
