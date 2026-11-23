-- request: POST /api/tk_notes
WITH __p_1 AS (
  SELECT DISTINCT "room_id" FROM "public"."tk_bookings"
  WHERE "room_id" = $1
),
__mut AS (
  INSERT INTO "public"."tk_notes" ("valid_at", "note", "room_id")
  SELECT $2, $3, __p_1."room_id"
  FROM __p_1
  RETURNING "id", "room_id", "valid_at", "note"
)
SELECT jsonb_build_object('row', jsonb_build_object('id', t0."id", 'room_id', t0."room_id", 'valid_at', t0."valid_at", 'note', t0."note"), 'key', jsonb_build_array(t0."id")) AS data
FROM __mut t0
-- args: [1,"[2026-01-02,2026-01-03)","late checkout"]
