-- request: GET /api/users?posts.title=sw.A&posts.title=ew.z&posts!none.author_id=eq.9&posts=exists.true
SELECT jsonb_build_object('rows', coalesce(jsonb_agg(s5.data ORDER BY s5.__rn ASC) FILTER (WHERE s5.__rn <= $5), '[]'::jsonb), 'has_next', coalesce(bool_or(s5.__rn > $5), false), 'has_prev', false, 'first', (jsonb_agg(s5.__key ORDER BY s5.__rn ASC) FILTER (WHERE s5.__rn <= $5))->0, 'last', (jsonb_agg(s5.__key ORDER BY s5.__rn ASC) FILTER (WHERE s5.__rn <= $5))->-1) AS data
FROM (
  SELECT jsonb_build_object('id', t0."id", 'email', t0."email", 'full_name', t0."full_name", 'mood', t0."mood", 'settings', t0."settings", 'tags', to_jsonb(t0."tags"), 'balance', (t0."balance")::text, 'created_at', t0."created_at", 'address', CASE WHEN (t0."address")::text IS NULL THEN NULL ELSE jsonb_build_object('street', (t0."address")."street", 'city', (t0."address")."city", 'mood', (t0."address")."mood") END, 'prev_addresses', CASE WHEN t0."prev_addresses" IS NULL THEN NULL ELSE (SELECT coalesce(jsonb_agg(CASE WHEN ((t0."prev_addresses")[e1.o])::text IS NULL THEN NULL ELSE jsonb_build_object('street', ((t0."prev_addresses")[e1.o])."street", 'city', ((t0."prev_addresses")[e1.o])."city", 'mood', ((t0."prev_addresses")[e1.o])."mood") END ORDER BY e1.o), '[]'::jsonb) FROM (SELECT generate_subscripts(t0."prev_addresses", 1) AS o) e1) END, 'moods', to_jsonb(t0."moods")) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."users" t0
  WHERE EXISTS (SELECT 1 FROM "public"."posts" r2 WHERE r2."author_id" = t0."id") AND NOT EXISTS (SELECT 1 FROM "public"."posts" r3 WHERE r3."author_id" = t0."id" AND (r3."author_id" = $1)) AND EXISTS (SELECT 1 FROM "public"."posts" r4 WHERE r4."author_id" = t0."id" AND ((r4."title" LIKE '%' || $2 AND r4."title" LIKE $3 || '%')))
  ORDER BY t0."id" ASC
  LIMIT $4
) s5
-- args: [9,"z","A",101,100]
