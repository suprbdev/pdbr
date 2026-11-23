-- request: POST /api/rpc/total_title_length
SELECT to_json(__fn.v) AS data
FROM (SELECT "public"."total_title_length"("ps" => CASE WHEN $1::jsonb IS NULL THEN NULL ELSE coalesce((SELECT array_agg(jsonb_populate_record(NULL::"public"."posts", NULLIF(e0.v, 'null'::jsonb)) ORDER BY e0.o) FROM jsonb_array_elements($1::jsonb) WITH ORDINALITY e0(v, o)), ARRAY[]::"public"."posts"[]) END) AS v) AS __fn
-- args: ["[{\"title\":\"abc\"},null,{\"id\":2,\"title\":\"de\"}]"]
