-- request: PUT /api/tk_rates
WITH __mut AS (
  INSERT INTO "public"."tk_rates" ("id", "room_id", "valid_at", "price")
  VALUES ($1, $2, $3, $4)
  ON CONFLICT ("id") DO UPDATE
  SET "room_id" = EXCLUDED."room_id", "valid_at" = EXCLUDED."valid_at", "price" = EXCLUDED."price"
  RETURNING "id", "room_id", "valid_at", "price", (xmax = 0) AS __inserted
)
SELECT json_build_object('row', json_build_object('id', t0."id", 'room_id', t0."room_id", 'valid_at', t0."valid_at", 'price', (t0."price")::text), 'inserted', t0.__inserted, 'key', jsonb_build_array(t0."id")) AS data
FROM __mut t0
-- args: [1,1,"[2026-01-01,2026-01-05)","10"]
