-- request: GET /api/memberships/7,eng?select=user_id,team,role
SELECT json_build_object('etag', __q.h || '.' || md5(__q.data::text), 'row', __q.data) AS data
FROM (
  SELECT md5((json_build_object('user_id', t0."user_id", 'team', t0."team", 'role', t0."role"))::text) AS h, json_build_object('user_id', t0."user_id", 'team', t0."team", 'role', t0."role") AS data
  FROM "public"."memberships" t0
  WHERE t0."user_id" = $1 AND t0."team" = $2
  LIMIT 1
) __q
-- args: [7,"eng"]
