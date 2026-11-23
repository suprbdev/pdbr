-- request: POST /api/rpc/user_greeting
SELECT to_json(__fn.v) AS data
FROM (SELECT "public"."user_greeting"("greeting" => $1::"text", "u" => jsonb_populate_record(NULL::"public"."users", $2::jsonb)) AS v) AS __fn
-- args: ["Hello","{\"address\":{\"city\":\"London\"},\"email\":\"ada@example.com\",\"id\":1,\"mood\":\"happy\",\"settings\":{\"theme\":\"dark\"},\"tags\":[\"a\",\"b\"]}"]
