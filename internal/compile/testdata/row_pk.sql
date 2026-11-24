-- request: GET /api/posts/7?select=id,title,users(email)
SELECT json_build_object('etag', __q.h || '.' || md5(__q.data::text), 'row', __q.data) AS data
FROM (
  SELECT md5((json_build_object('id', t0."id", 'author_id', t0."author_id", 'title', t0."title", 'body', t0."body", 'published', t0."published"))::text) AS h, json_build_object('id', t0."id", 'title', t0."title", 'users', l2.data) AS data
  FROM "public"."posts" t0
  LEFT JOIN LATERAL (
    SELECT json_build_object('email', t1."email") AS data
    FROM "public"."users" t1
    WHERE t1."id" = t0."author_id"
    LIMIT 1
  ) l2 ON true
  WHERE t0."id" = $1
  LIMIT 1
) __q
-- args: [7]
