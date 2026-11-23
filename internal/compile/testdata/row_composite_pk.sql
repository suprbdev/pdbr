-- request: GET /api/memberships/7,eng?select=user_id,team,role
SELECT json_build_object('etag', md5(to_jsonb(t0)::text), 'row', json_build_object('user_id', t0."user_id", 'team', t0."team", 'role', t0."role")) AS data
FROM "public"."memberships" t0
WHERE t0."user_id" = $1 AND t0."team" = $2
LIMIT 1
-- args: [7,"eng"]
