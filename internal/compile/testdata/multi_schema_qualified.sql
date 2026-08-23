-- request: GET /api/public/users/1?select=id
SELECT jsonb_build_object('etag', md5(to_jsonb(t0)::text), 'row', jsonb_build_object('id', t0."id")) AS data
FROM "public"."users" t0
WHERE t0."id" = $1
LIMIT 1
-- args: [1]
