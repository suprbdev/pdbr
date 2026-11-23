-- request: GET /api/tk_notes?select=id,tk_bookings!inner(guest)&tk_bookings.guest=eq.ada&tk_bookings.order=guest.desc&tk_bookings.limit=1
SELECT json_build_object('has_next', coalesce(bool_or(s5.__rn > $3), false), 'has_prev', false, 'first', (json_agg(s5.__key ORDER BY s5.__rn ASC) FILTER (WHERE s5.__rn <= $3))->0, 'last', (json_agg(s5.__key ORDER BY s5.__rn ASC) FILTER (WHERE s5.__rn <= $3))->-1, 'n', count(*) FILTER (WHERE s5.__rn <= $3), 'rows', coalesce(json_agg(s5.data ORDER BY s5.__rn ASC) FILTER (WHERE s5.__rn <= $3), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'tk_bookings', l3.data) AS data, jsonb_build_array(t0."id") AS __key, t0.__rn
  FROM (
    SELECT t0."id", t0."room_id", t0."valid_at", row_number() OVER (ORDER BY t0."id" ASC) AS __rn
    FROM "public"."tk_notes" t0
    WHERE EXISTS (SELECT 1 FROM "public"."tk_bookings" r4 WHERE r4."room_id" = t0."room_id" AND r4."valid_at" && t0."valid_at" AND r4."guest" = $2)
    ORDER BY t0."id" ASC
    LIMIT 101
  ) t0
  LEFT JOIN LATERAL (
    SELECT coalesce(json_agg(s2.data), '[]'::json) AS data
    FROM (
      SELECT json_build_object('guest', t1."guest") AS data
      FROM "public"."tk_bookings" t1
      WHERE t1."room_id" = t0."room_id" AND t1."valid_at" && t0."valid_at" AND t1."guest" = $1
      ORDER BY t1."guest" DESC, t1."room_id" ASC, t1."valid_at" ASC
      LIMIT 1
    ) s2
  ) l3 ON true
) s5
-- args: ["ada","ada",100]
