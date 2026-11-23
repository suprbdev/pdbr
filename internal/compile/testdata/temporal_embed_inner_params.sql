-- request: GET /api/tk_notes?select=id,tk_bookings!inner(guest)&tk_bookings.guest=eq.ada&tk_bookings.order=guest.desc&tk_bookings.limit=1
SELECT jsonb_build_object('rows', coalesce(jsonb_agg(s5.data ORDER BY s5.__rn ASC) FILTER (WHERE s5.__rn <= $5), '[]'::jsonb), 'has_next', coalesce(bool_or(s5.__rn > $5), false), 'has_prev', false, 'first', (jsonb_agg(s5.__key ORDER BY s5.__rn ASC) FILTER (WHERE s5.__rn <= $5))->0, 'last', (jsonb_agg(s5.__key ORDER BY s5.__rn ASC) FILTER (WHERE s5.__rn <= $5))->-1) AS data
FROM (
  SELECT jsonb_build_object('id', t0."id", 'tk_bookings', l3.data) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."tk_notes" t0
  LEFT JOIN LATERAL (
    SELECT coalesce(jsonb_agg(s2.data), '[]'::jsonb) AS data
    FROM (
      SELECT jsonb_build_object('guest', t1."guest") AS data
      FROM "public"."tk_bookings" t1
      WHERE t1."room_id" = t0."room_id" AND t1."valid_at" && t0."valid_at" AND t1."guest" = $1
      ORDER BY t1."guest" DESC, t1."room_id" ASC, t1."valid_at" ASC
      LIMIT $2
    ) s2
  ) l3 ON true
  WHERE EXISTS (SELECT 1 FROM "public"."tk_bookings" r4 WHERE r4."room_id" = t0."room_id" AND r4."valid_at" && t0."valid_at" AND r4."guest" = $3)
  ORDER BY t0."id" ASC
  LIMIT $4
) s5
-- args: ["ada",1,"ada",101,100]
