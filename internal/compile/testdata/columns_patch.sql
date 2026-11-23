-- request: PATCH /api/users?id=eq.1&columns=full_name&select=id,full_name
WITH __mut AS (
  UPDATE "public"."users" m0
  SET "full_name" = $2
  WHERE m0."id" = $1
  RETURNING "id", "full_name"
)
SELECT json_build_object('n', count(*), 'rows', coalesce(json_agg(s2.data), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t1."id", 'full_name', t1."full_name") AS data
  FROM __mut t1
) s2
-- args: [1,"B"]
