-- request: GET /api/users?select=id,tag_words&limit=2
SELECT json_build_object('has_next', coalesce(bool_or(s2.__rn > $2), false), 'has_prev', false, 'first', (json_agg(s2.__key ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $2))->0, 'last', (json_agg(s2.__key ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $2))->-1, 'n', count(*) FILTER (WHERE s2.__rn <= $2), 'rows', coalesce(json_agg(s2.data ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $2), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'tag_words', (SELECT coalesce(json_agg(v1.v), '[]'::json) FROM "public"."users_tag_words"("u" => ROW(t0."id", t0."email", t0."full_name", t0."mood", t0."settings", t0."tags", t0."balance", t0."created_at", t0."address", t0."prev_addresses", t0."moods")::"public"."users") v1(v))) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."users" t0
  ORDER BY t0."id" ASC
  LIMIT $1
) s2
-- args: [3,2]
