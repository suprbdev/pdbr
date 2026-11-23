-- request: GET /api/posts?select=id,users(id,settings->>theme)&users.settings->>theme=eq.dark&users.order=settings->n
SELECT json_build_object('has_next', coalesce(bool_or(s3.__rn > $5), false), 'has_prev', false, 'first', (json_agg(s3.__key ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $5))->0, 'last', (json_agg(s3.__key ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $5))->-1, 'n', count(*) FILTER (WHERE s3.__rn <= $5), 'rows', coalesce(json_agg(s3.data ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $5), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'users', l2.data) AS data, jsonb_build_array(t0."id") AS __key, t0.__rn
  FROM (
    SELECT t0."id", t0."author_id", row_number() OVER (ORDER BY t0."id" ASC) AS __rn
    FROM "public"."posts" t0
    ORDER BY t0."id" ASC
    LIMIT 101
  ) t0
  LEFT JOIN LATERAL (
    SELECT json_build_object('id', t1."id", $2::text, (t1."settings" ->> $1::text)) AS data
    FROM "public"."users" t1
    WHERE t1."id" = t0."author_id" AND (t1."settings" ->> $3::text) = $4
    LIMIT 1
  ) l2 ON true
) s3
-- args: ["theme","theme","theme","dark",100]
