-- request: POST /api/tk_bookings
WITH __mut AS (
  INSERT INTO "public"."tk_bookings" ("room_id", "valid_at", "guest")
  VALUES ($1, $2, $3)
  ON CONFLICT ON CONSTRAINT "tk_bookings_pkey" DO NOTHING
  RETURNING "room_id", "valid_at", "guest"
)
SELECT jsonb_build_object('room_id', t0."room_id", 'valid_at', t0."valid_at", 'guest', t0."guest") AS data
FROM __mut t0
-- args: [1,"[2026-01-02,2026-01-03)","dup"]
