-- request: POST /api/tk_bookings
WITH __mut AS (
  INSERT INTO "public"."tk_bookings" ("room_id", "valid_at", "guest")
  VALUES ($1, $2, $3)
  RETURNING "room_id", "valid_at", "guest"
),
__c_1 AS (
  INSERT INTO "public"."tk_notes" ("valid_at", "note", "room_id")
  SELECT $4, $5, __mut."room_id"
  FROM __mut
  RETURNING *
)
SELECT json_build_object('room_id', t0."room_id", 'valid_at', t0."valid_at", 'guest', t0."guest") AS data
FROM __mut t0
-- args: [1,"[2026-02-01,2026-02-05)","bo","[2026-02-01,2026-02-02)","allergies"]
