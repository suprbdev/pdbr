-- request: POST /api/rpc/vol_ints?limit=3&offset=1
WITH __call AS MATERIALIZED (SELECT __v.v FROM "public"."vol_ints"() AS __v(v))
SELECT coalesce(json_agg(to_json(__s.v)), '[]'::json) AS data
FROM (SELECT v FROM __call LIMIT 3 OFFSET $1) AS __s
-- args: [1]
