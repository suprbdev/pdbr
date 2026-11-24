-- request: POST /api/rpc/vol_ints?limit=0
WITH __call AS MATERIALIZED (SELECT __v.v FROM "public"."vol_ints"() AS __v(v))
SELECT '[]'::json AS data
FROM (SELECT count(*) FROM __call) AS __n
-- args: []
