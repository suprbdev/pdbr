-- request: GET /api/tk_notes?select=id,note,tk_bookings(guest,valid_at)
SELECT json_build_object('has_next', coalesce(bool_or(s4.__rn > $1), false), 'has_prev', false, 'first', (json_agg(s4.__key ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $1))->0, 'last', (json_agg(s4.__key ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $1))->-1, 'n', count(*) FILTER (WHERE s4.__rn <= $1), 'rows', coalesce(json_agg(s4.data ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $1), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'note', t0."note", 'tk_bookings', l3.data) AS data, jsonb_build_array(t0."id") AS __key, t0.__rn
  FROM (
    SELECT t0."id", t0."room_id", t0."valid_at", t0."note", row_number() OVER (ORDER BY t0."id" ASC) AS __rn
    FROM "public"."tk_notes" t0
    ORDER BY t0."id" ASC
    LIMIT 101
  ) t0
  LEFT JOIN LATERAL (
    SELECT coalesce(json_agg(s2.data), '[]'::json) AS data
    FROM (
      SELECT json_build_object('guest', t1."guest", 'valid_at', t1."valid_at") AS data
      FROM "public"."tk_bookings" t1
      WHERE t1."room_id" = t0."room_id" AND t1."valid_at" && t0."valid_at"
      ORDER BY t1."room_id" ASC, t1."valid_at" ASC
      LIMIT 100
    ) s2
  ) l3 ON true
) s4
-- args: [100]
