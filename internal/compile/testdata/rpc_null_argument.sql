-- request: POST /api/rpc/add_numbers
SELECT to_jsonb(__fn.v) AS data
FROM (SELECT "public"."add_numbers"("a" => $1::"int4", "b" => NULL::"int4") AS v) AS __fn
-- args: [1]
