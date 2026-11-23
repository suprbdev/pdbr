-- request: GET /api/posts?select=title,...users(*)
SELECT json_build_object('has_next', coalesce(bool_or(s4.__rn > $2), false), 'has_prev', false, 'first', (json_agg(s4.__key ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $2))->0, 'last', (json_agg(s4.__key ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $2))->-1, 'n', count(*) FILTER (WHERE s4.__rn <= $2), 'rows', coalesce(json_agg(s4.data ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $2), '[]'::json)) AS data
FROM (
  SELECT json_build_object('title', t0."title", 'id', l1.v0, 'email', l1.v1, 'full_name', l1.v2, 'mood', l1.v3, 'settings', l1.v4, 'tags', l1.v5, 'balance', l1.v6, 'created_at', l1.v7, 'address', l1.v8, 'prev_addresses', l1.v9, 'moods', l1.v10) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."posts" t0
  LEFT JOIN LATERAL (
    SELECT t2."id" AS v0, t2."email" AS v1, t2."full_name" AS v2, t2."mood" AS v3, t2."settings" AS v4, to_json(t2."tags") AS v5, (t2."balance")::text AS v6, t2."created_at" AS v7, CASE WHEN (t2."address")::text IS NULL THEN NULL ELSE json_build_object('street', (t2."address")."street", 'city', (t2."address")."city", 'mood', (t2."address")."mood") END AS v8, CASE WHEN t2."prev_addresses" IS NULL THEN NULL ELSE (SELECT coalesce(json_agg(CASE WHEN ((t2."prev_addresses")[e3.o])::text IS NULL THEN NULL ELSE json_build_object('street', ((t2."prev_addresses")[e3.o])."street", 'city', ((t2."prev_addresses")[e3.o])."city", 'mood', ((t2."prev_addresses")[e3.o])."mood") END ORDER BY e3.o), '[]'::json) FROM (SELECT generate_subscripts(t2."prev_addresses", 1) AS o) e3) END AS v9, to_json(t2."moods") AS v10
    FROM "public"."users" t2
    WHERE t2."id" = t0."author_id"
    LIMIT 1
  ) l1 ON true
  ORDER BY t0."id" ASC
  LIMIT $1
) s4
-- args: [101,100]
