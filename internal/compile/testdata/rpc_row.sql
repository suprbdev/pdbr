-- request: POST /api/rpc/publish_post?select=title,users(email)
SELECT CASE WHEN t0 IS NULL THEN NULL ELSE jsonb_build_object('title', t0."title", 'users', l2.data) END AS data
FROM "public"."publish_post"("post_id" => $1::"int4") AS t0
LEFT JOIN LATERAL (
  SELECT jsonb_build_object('email', t1."email") AS data
  FROM "public"."users" t1
  WHERE t1."id" = t0."author_id"
  LIMIT 1
) l2 ON true
-- args: [7]
