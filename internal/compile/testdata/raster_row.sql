-- request: GET /api/kb/tiles/1
SELECT json_build_object('etag', md5(to_jsonb(ROW(t0."id", t0."name"))::text), 'row', json_build_object('id', t0."id", 'name', t0."name")) AS data
FROM "kb"."tiles" t0
WHERE t0."id" = $1
LIMIT 1
-- args: [1]
