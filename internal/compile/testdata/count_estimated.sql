-- request: GET /api/posts?select=id
SELECT jsonb_build_object('rows', coalesce(jsonb_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $2), '[]'::jsonb), 'has_next', coalesce(bool_or(s1.__rn > $2), false), 'has_prev', false, 'first', (jsonb_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $2))->0, 'last', (jsonb_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $2))->-1, 'total', (SELECT CASE WHEN e6.n <= $3 THEN e6.n ELSE greatest((SELECT CASE WHEN pc3.reltuples >= 0 THEN pc3.reltuples::bigint ELSE (SELECT count(*) FROM "public"."posts" c2) END FROM pg_catalog.pg_class pc3 WHERE pc3.oid = '"public"."posts"'::regclass), $3 + 1) END FROM (SELECT (SELECT count(*) FROM (SELECT 1 FROM "public"."posts" c4 LIMIT $3 + 1) b5) AS n) e6)) AS data
FROM (
  SELECT jsonb_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."posts" t0
  ORDER BY t0."id" ASC
  LIMIT $1
) s1
-- args: [101,100,100]
