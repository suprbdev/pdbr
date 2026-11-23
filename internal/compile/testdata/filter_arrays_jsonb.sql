-- request: GET /api/users?tags=cs.{admin,founder}&tags=ov.{staff,guest}&moods=cd.{sad,ok,happy}&settings=cs.{"theme":"dark"}&settings=hk.theme&settings=jpe.$.theme&settings=jpm.$.theme=="dark"
SELECT json_build_object('rows', coalesce(json_agg(s2.data ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $9), '[]'::json), 'has_next', coalesce(bool_or(s2.__rn > $9), false), 'has_prev', false, 'first', (json_agg(s2.__key ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $9))->0, 'last', (json_agg(s2.__key ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $9))->-1) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'email', t0."email", 'full_name', t0."full_name", 'mood', t0."mood", 'settings', t0."settings", 'tags', to_json(t0."tags"), 'balance', (t0."balance")::text, 'created_at', t0."created_at", 'address', CASE WHEN (t0."address")::text IS NULL THEN NULL ELSE json_build_object('street', (t0."address")."street", 'city', (t0."address")."city", 'mood', (t0."address")."mood") END, 'prev_addresses', CASE WHEN t0."prev_addresses" IS NULL THEN NULL ELSE (SELECT coalesce(json_agg(CASE WHEN ((t0."prev_addresses")[e1.o])::text IS NULL THEN NULL ELSE json_build_object('street', ((t0."prev_addresses")[e1.o])."street", 'city', ((t0."prev_addresses")[e1.o])."city", 'mood', ((t0."prev_addresses")[e1.o])."mood") END ORDER BY e1.o), '[]'::json) FROM (SELECT generate_subscripts(t0."prev_addresses", 1) AS o) e1) END, 'moods', to_json(t0."moods")) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."users" t0
  WHERE t0."moods" <@ $1::text[]::"public"."mood"[] AND t0."settings" @> $2::jsonb AND t0."settings" ? $3 AND t0."settings" @? $4::jsonpath AND t0."settings" @@ $5::jsonpath AND t0."tags" @> $6::"text"[] AND t0."tags" && $7::"text"[]
  ORDER BY t0."id" ASC
  LIMIT $8
) s2
-- args: [["sad","ok","happy"],"{\"theme\":\"dark\"}","theme","$.theme","$.theme==\"dark\"",["admin","founder"],["staff","guest"],101,100]
