-- request: GET /api/rpc/mood_moods?limit=5&offset=1
SELECT coalesce(json_agg(to_json(__s.v)), '[]'::json) AS data
FROM (SELECT __v.v FROM "public"."mood_moods"() AS __v(v) LIMIT 5 OFFSET $1) AS __s
-- args: [1]
