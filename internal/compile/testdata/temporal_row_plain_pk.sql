-- request: GET /api/tk_rates/1
SELECT json_build_object('etag', md5(to_jsonb(t0)::text), 'row', json_build_object('id', t0."id", 'room_id', t0."room_id", 'valid_at', t0."valid_at", 'price', (t0."price")::text)) AS data
FROM "public"."tk_rates" t0
WHERE t0."id" = $1
LIMIT 1
-- args: [1]
