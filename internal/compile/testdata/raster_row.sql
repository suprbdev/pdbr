-- request: GET /api/kb/tiles/1
SELECT json_build_object('etag', __q.h || '.' || md5(__q.data::text), 'row', __q.data) AS data
FROM (
  SELECT md5((json_build_object('id', t0."id", 'name', t0."name"))::text) AS h, json_build_object('id', t0."id", 'name', t0."name") AS data
  FROM "kb"."tiles" t0
  WHERE t0."id" = $1
  LIMIT 1
) __q
-- args: [1]
