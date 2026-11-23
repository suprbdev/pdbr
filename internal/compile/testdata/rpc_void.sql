-- request: POST /api/rpc/noop
SELECT 'null'::json AS data FROM (SELECT "public"."noop"()) AS __v
-- args: []
