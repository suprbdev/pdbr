-- request: POST /api/users?select=id,address,prev_addresses
WITH __mut AS (
  INSERT INTO "public"."users" ("email", "address", "prev_addresses")
  VALUES ($1, jsonb_populate_record(NULL::"public"."address", $2::jsonb), CASE WHEN $3::jsonb IS NULL THEN NULL ELSE coalesce((SELECT array_agg(jsonb_populate_record(NULL::"public"."address", NULLIF(e0.v, 'null'::jsonb)) ORDER BY e0.o) FROM jsonb_array_elements($3::jsonb) WITH ORDINALITY e0(v, o)), ARRAY[]::"public"."address"[]) END)
  RETURNING "id", "address", "prev_addresses"
)
SELECT jsonb_build_object('row', jsonb_build_object('id', t1."id", 'address', CASE WHEN (t1."address")::text IS NULL THEN NULL ELSE jsonb_build_object('street', (t1."address")."street", 'city', (t1."address")."city", 'mood', (t1."address")."mood") END, 'prev_addresses', CASE WHEN t1."prev_addresses" IS NULL THEN NULL ELSE (SELECT coalesce(jsonb_agg(CASE WHEN ((t1."prev_addresses")[e2.o])::text IS NULL THEN NULL ELSE jsonb_build_object('street', ((t1."prev_addresses")[e2.o])."street", 'city', ((t1."prev_addresses")[e2.o])."city", 'mood', ((t1."prev_addresses")[e2.o])."mood") END ORDER BY e2.o), '[]'::jsonb) FROM (SELECT generate_subscripts(t1."prev_addresses", 1) AS o) e2) END), 'key', jsonb_build_array(t1."id")) AS data
FROM __mut t1
-- args: ["a@b.c","{\"city\":\"London\",\"mood\":\"happy\",\"street\":\"12 St James Sq\"}","[{\"city\":\"Surrey\",\"mood\":\"ok\",\"street\":\"1 Ockham Park\"}]"]
