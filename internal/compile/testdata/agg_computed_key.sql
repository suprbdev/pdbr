-- request: GET /api/users?select=post_count,count()&limit=3
SELECT json_build_object('has_next', coalesce(bool_or(s2.__rn > $1), false), 'has_prev', false, 'n', count(*) FILTER (WHERE s2.__rn <= $1), 'rows', coalesce(json_agg(s2.data ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $1), '[]'::json)) AS data
FROM (
  SELECT json_build_object('post_count', g1."__k0", 'count', to_json(g1."__a0")) AS data, row_number() OVER (ORDER BY g1."__k0" ASC) AS __rn
  FROM (
    SELECT to_jsonb(("public"."users_post_count"("u" => ROW(t0."id", t0."email", t0."full_name", t0."mood", t0."settings", t0."tags", t0."balance", t0."created_at", t0."address", t0."prev_addresses", t0."moods")::"public"."users"))::text) AS __k0, count(*) AS __a0
    FROM "public"."users" t0
    GROUP BY 1
  ) g1
  ORDER BY g1."__k0" ASC
  LIMIT 4
) s2
-- args: [3]
