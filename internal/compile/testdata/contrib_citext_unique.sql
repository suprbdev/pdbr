-- request: GET /api/kb/docs/by/email/Ada@Example.com?select=id,email
SELECT json_build_object('etag', md5(to_jsonb(ROW(t0."id", t0."title", t0."body", t0."search", t0."email", t0."attrs", t0."path", t0."paths", t0."pages", t0."valid", t0."slots", t0."net"))::text), 'row', json_build_object('id', t0."id", 'email', t0."email")) AS data
FROM "kb"."docs" t0
WHERE t0."email" OPERATOR("ext".=) $1::"ext".citext
LIMIT 1
-- args: ["Ada@Example.com"]
