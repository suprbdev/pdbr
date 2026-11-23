-- request: GET /api/tk_notes?select=id,note,tk_bookings(guest,valid_at)
SELECT jsonb_build_object('rows', coalesce(jsonb_agg(s4.data ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $3), '[]'::jsonb), 'has_next', coalesce(bool_or(s4.__rn > $3), false), 'has_prev', false, 'first', (jsonb_agg(s4.__key ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $3))->0, 'last', (jsonb_agg(s4.__key ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $3))->-1) AS data
FROM (
  SELECT jsonb_build_object('id', t0."id", 'note', t0."note", 'tk_bookings', l3.data) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."tk_notes" t0
  LEFT JOIN LATERAL (
    SELECT coalesce(jsonb_agg(s2.data), '[]'::jsonb) AS data
    FROM (
      SELECT jsonb_build_object('guest', t1."guest", 'valid_at', t1."valid_at") AS data
      FROM "public"."tk_bookings" t1
      WHERE t1."room_id" = t0."room_id" AND t1."valid_at" && t0."valid_at"
      ORDER BY t1."room_id" ASC, t1."valid_at" ASC
      LIMIT $1
    ) s2
  ) l3 ON true
  ORDER BY t0."id" ASC
  LIMIT $2
) s4
-- args: [100,101,100]
