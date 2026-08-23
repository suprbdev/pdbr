-- request: POST /api/rpc/word_lengths
SELECT to_jsonb(__fn.v) AS data
FROM (SELECT "public"."word_lengths"($1::"text"[]) AS v) AS __fn
-- args: [["a","bb"]]
