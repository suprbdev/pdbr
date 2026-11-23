-- request: GET /api/tk_readings?select=id,tk_rooms(name)
SELECT json_build_object('has_next', coalesce(bool_or(s3.__rn > $1), false), 'has_prev', false, 'first', (json_agg(s3.__key ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $1))->0, 'last', (json_agg(s3.__key ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $1))->-1, 'n', count(*) FILTER (WHERE s3.__rn <= $1), 'rows', coalesce(json_agg(s3.data ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $1), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'tk_rooms', l2.data) AS data, jsonb_build_array(t0."id") AS __key, t0.__rn
  FROM (
    SELECT t0."id", t0."room_id", row_number() OVER (ORDER BY t0."id" ASC) AS __rn
    FROM "public"."tk_readings" t0
    ORDER BY t0."id" ASC
    LIMIT 101
  ) t0
  LEFT JOIN LATERAL (
    SELECT json_build_object('name', t1."name") AS data
    FROM "public"."tk_rooms" t1
    WHERE t1."id" = t0."room_id"
    LIMIT 1
  ) l2 ON true
) s3
-- args: [100]
