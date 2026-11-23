-- request: PATCH /api/tk_notes/1?select=id,tk_bookings(guest)
WITH __p_1 AS (
  UPDATE "public"."tk_bookings"
  SET "guest" = $2
  WHERE EXISTS (SELECT 1 FROM "public"."tk_notes" __o1 WHERE __o1."id" = $1 AND __o1."room_id" = "public"."tk_bookings"."room_id" AND __o1."valid_at" && "public"."tk_bookings"."valid_at")
  RETURNING "public"."tk_bookings".*
),
__mut AS (
  SELECT "id", "room_id", "valid_at" FROM "public"."tk_notes"
  WHERE "public"."tk_notes"."id" = $1
  FOR UPDATE
),
__vis_1 AS NOT MATERIALIZED (
  SELECT "room_id", "valid_at", "guest" FROM "public"."tk_bookings" WHERE (EXISTS (SELECT 1 FROM "public"."tk_notes" __o1 WHERE __o1."id" = $1 AND __o1."room_id" = "public"."tk_bookings"."room_id" AND __o1."valid_at" && "public"."tk_bookings"."valid_at")) IS NOT TRUE
  UNION ALL SELECT "room_id", "valid_at", "guest" FROM __p_1
),
__guard AS MATERIALIZED (
  SELECT (SELECT CASE WHEN count(*) > 0 THEN true ELSE concat('pdbr guard: row of "tk_notes": matched ', count(*), ' rows')::boolean END FROM __mut)
    AND (SELECT CASE WHEN count(*) > 0 THEN true ELSE concat('pdbr guard: nested "tk_bookings.update": matched ', count(*), ' rows')::boolean END FROM __p_1) AS ok
)
SELECT __guarded.data
FROM __guard
LEFT JOIN LATERAL (
  SELECT json_build_object('id', t0."id", 'tk_bookings', l3.data) AS data
  FROM __mut t0
  LEFT JOIN LATERAL (
    SELECT coalesce(json_agg(s2.data), '[]'::json) AS data
    FROM (
      SELECT json_build_object('guest', t1."guest") AS data
      FROM __vis_1 t1
      WHERE t1."room_id" = t0."room_id" AND t1."valid_at" && t0."valid_at"
      ORDER BY t1."room_id" ASC, t1."valid_at" ASC
      LIMIT 100
    ) s2
  ) l3 ON true
) __guarded ON true
WHERE __guard.ok
-- args: [1,"Bo"]
