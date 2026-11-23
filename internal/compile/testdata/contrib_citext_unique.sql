-- request: GET /api/kb/docs/by/email/Ada@Example.com?select=id,email
SELECT json_build_object('etag', __q.h || '.' || md5(__q.data::text), 'row', __q.data) AS data
FROM (
  SELECT md5((json_build_object('id', t0."id", 'title', t0."title", 'body', t0."body", 'search', t0."search", 'email', t0."email", 'attrs', t0."attrs", 'path', t0."path", 'paths', to_json(t0."paths"), 'pages', t0."pages", 'valid', t0."valid", 'slots', t0."slots", 'net', t0."net"))::text) AS h, json_build_object('id', t0."id", 'email', t0."email") AS data
  FROM "kb"."docs" t0
  WHERE t0."email" OPERATOR("ext".=) $1::"ext".citext
  LIMIT 1
) __q
-- args: ["Ada@Example.com"]
