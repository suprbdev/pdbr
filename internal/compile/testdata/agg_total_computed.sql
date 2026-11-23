-- request: GET /api/users?select=mood,post_count,count()&limit=2
SELECT jsonb_build_object('rows', coalesce(jsonb_agg(s2.data ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $2), '[]'::jsonb), 'has_next', coalesce(bool_or(s2.__rn > $2), false), 'has_prev', false, 'total', (SELECT count(*) FROM (SELECT 1 FROM "public"."users" c3 GROUP BY c3."mood", to_jsonb(("public"."users_post_count"("u" => ROW(c3."id", c3."email", c3."full_name", c3."mood", c3."settings", c3."tags", c3."balance", c3."created_at", c3."address", c3."prev_addresses", c3."moods")::"public"."users"))::text)) g4)) AS data
FROM (
  SELECT jsonb_build_object('mood', g1."__k0", 'post_count', g1."__k1", 'count', to_jsonb(g1."__a0")) AS data, row_number() OVER (ORDER BY g1."__k0" ASC, g1."__k1" ASC) AS __rn
  FROM (
    SELECT t0."mood" AS __k0, to_jsonb(("public"."users_post_count"("u" => ROW(t0."id", t0."email", t0."full_name", t0."mood", t0."settings", t0."tags", t0."balance", t0."created_at", t0."address", t0."prev_addresses", t0."moods")::"public"."users"))::text) AS __k1, count(*) AS __a0
    FROM "public"."users" t0
    GROUP BY 1, 2
  ) g1
  ORDER BY g1."__k0" ASC, g1."__k1" ASC
  LIMIT $1
) s2
-- args: [3,2]
