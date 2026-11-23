-- request: GET /api/users?select=id,...posts(post_ids:id,titles:title)&posts.order=id.desc&posts.limit=5&posts.title=neq.x
SELECT json_build_object('has_next', coalesce(bool_or(s4.__rn > $6), false), 'has_prev', false, 'first', (json_agg(s4.__key ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $6))->0, 'last', (json_agg(s4.__key ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $6))->-1, 'n', count(*) FILTER (WHERE s4.__rn <= $6), 'rows', coalesce(json_agg(s4.data ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $6), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", $1::text, l1.v0, $2::text, l1.v1) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."users" t0
  LEFT JOIN LATERAL (
    SELECT coalesce(json_agg(s3.v0), '[]'::json) AS v0, coalesce(json_agg(s3.v1), '[]'::json) AS v1
    FROM (
      SELECT t2."id" AS v0, t2."title" AS v1
      FROM "public"."posts" t2
      WHERE t2."author_id" = t0."id" AND t2."title" <> $3
      ORDER BY t2."id" DESC
      LIMIT $4
    ) s3
  ) l1 ON true
  ORDER BY t0."id" ASC
  LIMIT $5
) s4
-- args: ["post_ids","titles","x",5,101,100]
