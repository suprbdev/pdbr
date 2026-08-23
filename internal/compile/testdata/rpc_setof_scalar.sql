-- request: GET /api/rpc/mood_moods?limit=5&offset=1
SELECT coalesce(jsonb_agg(to_jsonb(__s.v)), '[]'::jsonb) AS data
FROM (SELECT __v.v FROM "public"."mood_moods"() AS __v(v) LIMIT $1 OFFSET $2) AS __s
-- args: [5,1]
