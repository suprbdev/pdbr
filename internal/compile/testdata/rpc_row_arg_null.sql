-- request: POST /api/rpc/user_greeting
SELECT to_json(__fn.v) AS data
FROM (SELECT "public"."user_greeting"("greeting" => $1::"text", "u" => NULL::"public"."users") AS v) AS __fn
-- args: ["Hi"]
