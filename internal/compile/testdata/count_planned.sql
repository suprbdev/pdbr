-- request: GET /api/users?limit=2
SELECT jsonb_build_object('rows', coalesce(jsonb_agg(s2.data ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $2), '[]'::jsonb), 'has_next', coalesce(bool_or(s2.__rn > $2), false), 'has_prev', false, 'first', (jsonb_agg(s2.__key ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $2))->0, 'last', (jsonb_agg(s2.__key ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $2))->-1, 'total', (SELECT CASE WHEN pc4.reltuples >= 0 THEN pc4.reltuples::bigint ELSE (SELECT count(*) FROM "public"."users" c3) END FROM pg_catalog.pg_class pc4 WHERE pc4.oid = '"public"."users"'::regclass)) AS data
FROM (
  SELECT jsonb_build_object('id', t0."id", 'email', t0."email", 'full_name', t0."full_name", 'mood', t0."mood", 'settings', t0."settings", 'tags', to_jsonb(t0."tags"), 'balance', (t0."balance")::text, 'created_at', t0."created_at", 'address', CASE WHEN (t0."address")::text IS NULL THEN NULL ELSE jsonb_build_object('street', (t0."address")."street", 'city', (t0."address")."city", 'mood', (t0."address")."mood") END, 'prev_addresses', CASE WHEN t0."prev_addresses" IS NULL THEN NULL ELSE (SELECT coalesce(jsonb_agg(CASE WHEN ((t0."prev_addresses")[e1.o])::text IS NULL THEN NULL ELSE jsonb_build_object('street', ((t0."prev_addresses")[e1.o])."street", 'city', ((t0."prev_addresses")[e1.o])."city", 'mood', ((t0."prev_addresses")[e1.o])."mood") END ORDER BY e1.o), '[]'::jsonb) FROM (SELECT generate_subscripts(t0."prev_addresses", 1) AS o) e1) END, 'moods', to_jsonb(t0."moods")) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."users" t0
  ORDER BY t0."id" ASC
  LIMIT $1
) s2
-- args: [3,2]
