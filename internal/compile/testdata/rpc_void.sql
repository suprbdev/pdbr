-- request: POST /api/rpc/noop
SELECT 'null'::jsonb AS data FROM (SELECT "public"."noop"()) AS __v
-- args: []
