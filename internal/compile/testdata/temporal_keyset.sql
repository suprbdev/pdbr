-- request: GET /api/tk_bookings?after=WyJwdWJsaWMudGtfYm9va2luZ3MiLDEsIlsyMDI2LTAxLTAxLDIwMjYtMDEtMDUpIl0
SELECT jsonb_build_object('rows', coalesce(jsonb_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4), '[]'::jsonb), 'has_next', coalesce(bool_or(s1.__rn > $4), false), 'has_prev', true, 'first', (jsonb_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4))->0, 'last', (jsonb_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4))->-1) AS data
FROM (
  SELECT jsonb_build_object('room_id', t0."room_id", 'valid_at', t0."valid_at", 'guest', t0."guest") AS data, jsonb_build_array(t0."room_id", t0."valid_at") AS __key, row_number() OVER (ORDER BY t0."room_id" ASC, t0."valid_at" ASC) AS __rn
  FROM "public"."tk_bookings" t0
  WHERE (t0."room_id", t0."valid_at") > ($1, $2)
  ORDER BY t0."room_id" ASC, t0."valid_at" ASC
  LIMIT $3
) s1
-- args: [1,"[2026-01-01,2026-01-05)",101,100]
