-- request: GET /api/rpc/mood_counts?select=mood,n&n=gt.0&order=n.desc,mood&limit=5&count=true
SELECT jsonb_build_object('rows', coalesce(jsonb_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $3), '[]'::jsonb), 'has_next', coalesce(bool_or(s1.__rn > $3), false), 'has_prev', false, 'total', (SELECT count(*) FROM "public"."mood_counts"() c2 WHERE c2."n" > $4)) AS data
FROM (
  SELECT jsonb_build_object('mood', t0."mood", 'n', (t0."n")::text) AS data, row_number() OVER (ORDER BY t0."n" DESC NULLS LAST, t0."mood" ASC) AS __rn
  FROM "public"."mood_counts"() t0
  WHERE t0."n" > $1
  ORDER BY t0."n" DESC NULLS LAST, t0."mood" ASC
  LIMIT $2
) s1
-- args: [0,6,5,0]
