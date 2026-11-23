-- request: POST /api/users?columns=email,full_name&select=id,email
WITH __mut AS (
  INSERT INTO "public"."users" ("email", "full_name")
  VALUES ($1, $2), ($3, DEFAULT)
  RETURNING "id", "email"
)
SELECT coalesce(json_agg(s1.data), '[]'::json) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'email', t0."email") AS data
  FROM __mut t0
) s1
-- args: ["a@example.com","A","b@example.com"]
