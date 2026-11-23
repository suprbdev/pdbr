-- request: GET /api/rpc/user_greeting?greeting=Hi&u=%7B%22email%22%3A%22ada%40example.com%22%7D
SELECT to_json(__fn.v) AS data
FROM (SELECT "public"."user_greeting"("greeting" => $1::"text", "u" => jsonb_populate_record(NULL::"public"."users", $2::jsonb)) AS v) AS __fn
-- args: ["Hi","{\"email\":\"ada@example.com\"}"]
