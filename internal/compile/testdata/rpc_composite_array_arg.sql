-- request: POST /api/rpc/address_cities
SELECT to_json(__fn.v) AS data
FROM (SELECT "public"."address_cities"("addrs" => CASE WHEN $1::jsonb IS NULL THEN NULL ELSE coalesce((SELECT array_agg(jsonb_populate_record(NULL::"public"."address", NULLIF(e0.v, 'null'::jsonb)) ORDER BY e0.o) FROM jsonb_array_elements($1::jsonb) WITH ORDINALITY e0(v, o)), ARRAY[]::"public"."address"[]) END) AS v) AS __fn
-- args: ["[{\"city\":\"London\"},{\"city\":\"Paris\",\"street\":\"x\"}]"]
