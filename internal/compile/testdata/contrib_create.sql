-- request: POST /api/kb/docs
WITH __mut AS (
  INSERT INTO "kb"."docs" ("id", "title", "search", "email", "attrs", "path", "paths", "pages", "valid", "slots", "net")
  VALUES ($1, $2, $3, $4, "ext".hstore($5::text[]), $6, $7::text[]::"ext"."ltree"[], int4range($8::int4, $9::int4, $10::text), $11, int4multirange(VARIADIC ARRAY(SELECT int4range(__l, __u, __b) FROM unnest($12::int4[], $13::int4[], $14::text[]) AS __r(__l, __u, __b))), $15)
  RETURNING "id", "title", "body", "search", "email", "attrs", "path", "paths", "pages", "valid", "slots", "net"
)
SELECT jsonb_build_object('row', jsonb_build_object('id', t0."id", 'title', t0."title", 'body', t0."body", 'search', t0."search", 'email', t0."email", 'attrs', t0."attrs", 'path', t0."path", 'paths', to_jsonb(t0."paths"), 'pages', t0."pages", 'valid', t0."valid", 'slots', t0."slots", 'net', t0."net"), 'key', jsonb_build_array(t0."id")) AS data
FROM __mut t0
-- args: [9,"t","fat:1 cat:2","X@Y.Z",["color","red","size",null],"top.a",["a","b.c"],"1","5","[]","[2020-01-01,2020-02-01)",["1","5"],["3","7"],["[)","[)"],"10.0.0.1"]
