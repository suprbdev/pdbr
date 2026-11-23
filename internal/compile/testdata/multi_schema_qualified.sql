-- request: GET /api/public/users/1?select=id
SELECT json_build_object('etag', md5(to_jsonb(ROW(t0."id", t0."email", t0."full_name", t0."mood", t0."settings", t0."tags", t0."balance", t0."created_at", t0."address", t0."prev_addresses", t0."moods"))::text), 'row', json_build_object('id', t0."id")) AS data
FROM "public"."users" t0
WHERE t0."id" = $1
LIMIT 1
-- args: [1]
