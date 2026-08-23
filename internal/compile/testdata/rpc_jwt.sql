-- request: POST /api/rpc/authenticate
SELECT to_jsonb(__fn.v) AS data
FROM (SELECT "public"."authenticate"($1::"text") AS v) AS __fn
-- args: ["ada@example.com"]
