-- request: GET /api/tk_notes?select=id&tk_bookings!every.guest=eq.ada&order=tk_bookings(guest).desc
SELECT json_build_object('rows', coalesce(json_agg(s2.data ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $3), '[]'::json), 'has_next', coalesce(bool_or(s2.__rn > $3), false), 'has_prev', false, 'first', (json_agg(s2.__key ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $3))->0, 'last', (json_agg(s2.__key ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $3))->-1) AS data
FROM (
  SELECT json_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY (SELECT MAX(__ro."guest") FROM "public"."tk_bookings" __ro WHERE __ro."room_id" = t0."room_id" AND __ro."valid_at" && t0."valid_at") DESC NULLS LAST, t0."id" ASC) AS __rn
  FROM "public"."tk_notes" t0
  WHERE NOT EXISTS (SELECT 1 FROM "public"."tk_bookings" r1 WHERE r1."room_id" = t0."room_id" AND r1."valid_at" && t0."valid_at" AND NOT (r1."guest" = $1))
  ORDER BY (SELECT MAX(__ro."guest") FROM "public"."tk_bookings" __ro WHERE __ro."room_id" = t0."room_id" AND __ro."valid_at" && t0."valid_at") DESC NULLS LAST, t0."id" ASC
  LIMIT $2
) s2
-- args: ["ada",101,100]
