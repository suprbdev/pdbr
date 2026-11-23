-- request: PATCH /api/tk_bookings?room_id=eq.2&guest=eq.cy
WITH __mut AS (
  UPDATE "public"."tk_bookings" m0
  SET "guest" = $3
  WHERE m0."guest" = $1 AND m0."room_id" = $2
  RETURNING "room_id", "valid_at", "guest"
)
SELECT json_build_object('n', count(*), 'rows', coalesce(json_agg(s2.data), '[]'::json)) AS data
FROM (
  SELECT json_build_object('room_id', t1."room_id", 'valid_at', t1."valid_at", 'guest', t1."guest") AS data
  FROM __mut t1
) s2
-- args: ["cy",2,"zed"]
