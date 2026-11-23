-- request: POST /api/rpc/word_lengths
SELECT to_json(__fn.v) AS data
FROM (SELECT "public"."word_lengths"("words" => $1::"text"[]) AS v) AS __fn
-- args: [["a","bb"]]
