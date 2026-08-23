-- request: GET /api/users?mood=eq.happy&mood=in.(ok,happy)&moods=cs.{sad}&mood=not.eq.sad
SELECT jsonb_build_object('rows', coalesce(jsonb_agg(s2.data ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $6), '[]'::jsonb), 'has_next', coalesce(bool_or(s2.__rn > $6), false), 'has_prev', false, 'first', (jsonb_agg(s2.__key ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $6))->0, 'last', (jsonb_agg(s2.__key ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $6))->-1) AS data
FROM (
  SELECT jsonb_build_object('id', t0."id", 'email', t0."email", 'full_name', t0."full_name", 'mood', t0."mood", 'settings', t0."settings", 'tags', to_jsonb(t0."tags"), 'balance', (t0."balance")::text, 'created_at', t0."created_at", 'address', CASE WHEN (t0."address")::text IS NULL THEN NULL ELSE jsonb_build_object('street', (t0."address")."street", 'city', (t0."address")."city", 'mood', (t0."address")."mood") END, 'prev_addresses', CASE WHEN t0."prev_addresses" IS NULL THEN NULL ELSE (SELECT coalesce(jsonb_agg(CASE WHEN ((t0."prev_addresses")[e1.o])::text IS NULL THEN NULL ELSE jsonb_build_object('street', ((t0."prev_addresses")[e1.o])."street", 'city', ((t0."prev_addresses")[e1.o])."city", 'mood', ((t0."prev_addresses")[e1.o])."mood") END ORDER BY e1.o), '[]'::jsonb) FROM (SELECT generate_subscripts(t0."prev_addresses", 1) AS o) e1) END, 'moods', to_jsonb(t0."moods")) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."users" t0
  WHERE t0."mood" = $1 AND NOT (t0."mood" = $2) AND t0."mood" = ANY($3::text[]::"public"."mood"[]) AND t0."moods" @> $4::text[]::"public"."mood"[]
  ORDER BY t0."id" ASC
  LIMIT $5
) s2
-- args: ["happy","sad",["ok","happy"],["sad"],101,100]
