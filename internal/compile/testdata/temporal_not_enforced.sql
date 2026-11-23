-- request: GET /api/tk_readings?select=id,tk_rooms(name)
SELECT jsonb_build_object('rows', coalesce(jsonb_agg(s3.data ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $2), '[]'::jsonb), 'has_next', coalesce(bool_or(s3.__rn > $2), false), 'has_prev', false, 'first', (jsonb_agg(s3.__key ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $2))->0, 'last', (jsonb_agg(s3.__key ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $2))->-1) AS data
FROM (
  SELECT jsonb_build_object('id', t0."id", 'tk_rooms', l2.data) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."tk_readings" t0
  LEFT JOIN LATERAL (
    SELECT jsonb_build_object('name', t1."name") AS data
    FROM "public"."tk_rooms" t1
    WHERE t1."id" = t0."room_id"
    LIMIT 1
  ) l2 ON true
  ORDER BY t0."id" ASC
  LIMIT $1
) s3
-- args: [101,100]
