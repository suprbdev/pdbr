-- request: POST /api/tk_notes?select=id,tk_bookings(guest)
WITH __p_1 AS (
  INSERT INTO "public"."tk_bookings" ("room_id", "valid_at", "guest")
  VALUES ($1, $2, $3)
  RETURNING *
),
__mut AS (
  INSERT INTO "public"."tk_notes" ("valid_at", "note", "room_id")
  SELECT $4, $5, __p_1."room_id"
  FROM __p_1
  RETURNING "id", "room_id", "valid_at"
),
__vis_2 AS (
  SELECT * FROM "public"."tk_bookings" UNION ALL SELECT * FROM __p_1
)
SELECT json_build_object('row', json_build_object('id', t0."id", 'tk_bookings', l3.data), 'key', jsonb_build_array(t0."id")) AS data
FROM __mut t0
LEFT JOIN LATERAL (
  SELECT coalesce(json_agg(s2.data), '[]'::json) AS data
  FROM (
    SELECT json_build_object('guest', t1."guest") AS data
    FROM __vis_2 t1
    WHERE t1."room_id" = t0."room_id" AND t1."valid_at" && t0."valid_at"
    ORDER BY t1."room_id" ASC, t1."valid_at" ASC
    LIMIT $6
  ) s2
) l3 ON true
-- args: [1,"[2026-03-01,2026-03-05)","cy","[2026-03-01,2026-03-10)","x",100]
