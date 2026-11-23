-- request: GET /api/rpc/user_stats?uid=1
SELECT jsonb_build_object('posts', (t0."posts")::text, 'last_title', t0."last_title") AS data
FROM "public"."user_stats"("uid" => $1::"int4") AS t0
-- args: [1]
