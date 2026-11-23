-- request: GET /api/users?posts!every.title=sw.C&places!none.owner_id=eq.1&select=id&order=posts(title).desc,id&limit=3
SELECT json_build_object('rows', coalesce(json_agg(s3.data ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $4), '[]'::json), 'has_next', coalesce(bool_or(s3.__rn > $4), false), 'has_prev', false, 'first', (json_agg(s3.__key ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $4))->0, 'last', (json_agg(s3.__key ORDER BY s3.__rn ASC) FILTER (WHERE s3.__rn <= $4))->-1) AS data
FROM (
  SELECT json_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY (SELECT MAX(__ro."title") FROM "public"."posts" __ro WHERE __ro."author_id" = t0."id") DESC NULLS LAST, t0."id" ASC) AS __rn
  FROM "public"."users" t0
  WHERE NOT EXISTS (SELECT 1 FROM "public"."places" r1 WHERE r1."owner_id" = t0."id" AND (r1."owner_id" = $1)) AND NOT EXISTS (SELECT 1 FROM "public"."posts" r2 WHERE r2."author_id" = t0."id" AND NOT (r2."title" LIKE $2 || '%'))
  ORDER BY (SELECT MAX(__ro."title") FROM "public"."posts" __ro WHERE __ro."author_id" = t0."id") DESC NULLS LAST, t0."id" ASC
  LIMIT $3
) s3
-- args: [1,"C",4,3]
