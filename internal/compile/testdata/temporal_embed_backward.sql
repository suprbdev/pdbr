-- request: GET /api/tk_bookings?select=guest,tk_notes(note)
SELECT json_build_object('rows', coalesce(json_agg(s4.data ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $3), '[]'::json), 'has_next', coalesce(bool_or(s4.__rn > $3), false), 'has_prev', false, 'first', (json_agg(s4.__key ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $3))->0, 'last', (json_agg(s4.__key ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $3))->-1) AS data
FROM (
  SELECT json_build_object('guest', t0."guest", 'tk_notes', l3.data) AS data, jsonb_build_array(t0."room_id", t0."valid_at") AS __key, row_number() OVER (ORDER BY t0."room_id" ASC, t0."valid_at" ASC) AS __rn
  FROM "public"."tk_bookings" t0
  LEFT JOIN LATERAL (
    SELECT coalesce(json_agg(s2.data), '[]'::json) AS data
    FROM (
      SELECT json_build_object('note', t1."note") AS data
      FROM "public"."tk_notes" t1
      WHERE t1."room_id" = t0."room_id" AND t1."valid_at" && t0."valid_at"
      ORDER BY t1."id" ASC
      LIMIT $1
    ) s2
  ) l3 ON true
  ORDER BY t0."room_id" ASC, t0."valid_at" ASC
  LIMIT $2
) s4
-- args: [100,101,100]
