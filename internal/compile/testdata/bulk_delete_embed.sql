-- request: DELETE /api/posts?or=(author_id.eq.2,title.like.M3*)&select=id,title,users(email)
WITH __mut AS (
  DELETE FROM "public"."posts" m0
  WHERE (m0."author_id" = $1 OR m0."title" LIKE $2)
  RETURNING "id", "author_id", "title"
)
SELECT jsonb_build_object('n', count(*), 'rows', coalesce(jsonb_agg(s4.data), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', t1."id", 'title', t1."title", 'users', l3.data) AS data
  FROM __mut t1
  LEFT JOIN LATERAL (
    SELECT jsonb_build_object('email', t2."email") AS data
    FROM "public"."users" t2
    WHERE t2."id" = t1."author_id"
    LIMIT 1
  ) l3 ON true
) s4
-- args: [2,"M3%"]
