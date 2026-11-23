-- request: DELETE /api/posts?select=id,users!inner(email)&users.email=eq.ada@example.com
WITH __mut AS (
  DELETE FROM "public"."posts" m0
  WHERE EXISTS (SELECT 1 FROM "public"."users" r1 WHERE r1."id" = m0."author_id" AND r1."email" = $1)
  RETURNING "id", "author_id"
)
SELECT json_build_object('n', count(*), 'rows', coalesce(json_agg(s5.data), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t2."id", 'users', l4.data) AS data
  FROM __mut t2
  LEFT JOIN LATERAL (
    SELECT json_build_object('email', t3."email") AS data
    FROM "public"."users" t3
    WHERE t3."id" = t2."author_id" AND t3."email" = $2
    LIMIT 1
  ) l4 ON true
) s5
-- args: ["ada@example.com","ada@example.com"]
