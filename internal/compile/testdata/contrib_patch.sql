-- request: PATCH /api/kb/docs/9
WITH __mut AS (
  UPDATE "kb"."docs"
  SET "attrs" = $1, "pages" = $2, "valid" = daterange($3::date, $4::date, $5::text), "slots" = $6
  WHERE "kb"."docs"."id" = $7
  RETURNING "id", "title", "body", "search", "email", "attrs", "path", "paths", "pages", "valid", "slots", "net"
)
SELECT json_build_object('id', t0."id", 'title', t0."title", 'body', t0."body", 'search', t0."search", 'email', t0."email", 'attrs', t0."attrs", 'path', t0."path", 'paths', to_json(t0."paths"), 'pages', t0."pages", 'valid', t0."valid", 'slots', t0."slots", 'net', t0."net") AS data
FROM __mut t0
-- args: ["a=\u003e1","[2,4)","2000-01-01","2000-01-01","[)",null,9]
