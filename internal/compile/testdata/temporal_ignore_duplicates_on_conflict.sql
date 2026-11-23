-- request: POST /api/tk_rates?on_conflict=room_id,valid_at
WITH __mut AS (
  INSERT INTO "public"."tk_rates" ("id", "room_id", "valid_at", "price")
  VALUES ($1, $2, $3, $4)
  ON CONFLICT ON CONSTRAINT "tk_rates_room_id_valid_at_key" DO NOTHING
  RETURNING "id", "room_id", "valid_at", "price"
)
SELECT json_build_object('key', jsonb_build_array(t0."id"), 'row', json_build_object('id', t0."id", 'room_id', t0."room_id", 'valid_at', t0."valid_at", 'price', (t0."price")::text)) AS data
FROM __mut t0
-- args: [2,1,"[2026-01-02,2026-01-03)","1"]
