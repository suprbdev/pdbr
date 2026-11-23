-- request: GET /api/users?select=id,n:settings->n::int,settings->>theme::text,mood::text,balance::int8,*
SELECT json_build_object('has_next', coalesce(bool_or(s2.__rn > $5), false), 'has_prev', false, 'first', (json_agg(s2.__key ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $5))->0, 'last', (json_agg(s2.__key ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $5))->-1, 'n', count(*) FILTER (WHERE s2.__rn <= $5), 'rows', coalesce(json_agg(s2.data ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $5), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'email', t0."email", 'full_name', t0."full_name", 'mood', to_json((t0."mood")::"pg_catalog"."text"), 'settings', t0."settings", 'tags', to_json(t0."tags"), 'balance', to_json((t0."balance")::"pg_catalog"."int8"), 'created_at', t0."created_at", 'address', CASE WHEN (t0."address")::text IS NULL THEN NULL ELSE json_build_object('street', (t0."address")."street", 'city', (t0."address")."city", 'mood', (t0."address")."mood") END, 'prev_addresses', CASE WHEN t0."prev_addresses" IS NULL THEN NULL ELSE (SELECT coalesce(json_agg(CASE WHEN ((t0."prev_addresses")[e1.o])::text IS NULL THEN NULL ELSE json_build_object('street', ((t0."prev_addresses")[e1.o])."street", 'city', ((t0."prev_addresses")[e1.o])."city", 'mood', ((t0."prev_addresses")[e1.o])."mood") END ORDER BY e1.o), '[]'::json) FROM (SELECT generate_subscripts(t0."prev_addresses", 1) AS o) e1) END, 'moods', to_json(t0."moods"), 'n', to_json(((t0."settings" -> $1::text))::"pg_catalog"."int4"), $3::text, to_json(((t0."settings" ->> $2::text))::"pg_catalog"."text")) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."users" t0
  ORDER BY t0."id" ASC
  LIMIT $4
) s2
-- args: ["n","theme","theme",101,100]
