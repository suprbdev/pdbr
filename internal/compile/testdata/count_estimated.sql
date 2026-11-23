-- request: GET /api/users?select=id
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $2), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $2))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $2))->-1, 'total', (SELECT CASE WHEN e8.n <= $3 THEN e8.n ELSE greatest((SELECT CASE WHEN pc3.relkind = 'p' THEN coalesce((SELECT CASE WHEN bool_and(pl4.relkind = 'r' AND pl4.reltuples >= 0) THEN sum(pl4.reltuples)::bigint END FROM pg_catalog.pg_partition_tree(pc3.oid) pt5 JOIN pg_catalog.pg_class pl4 ON pl4.oid = pt5.relid WHERE pt5.isleaf), (SELECT count(*) FROM "public"."users" c2)) WHEN pc3.relhassubclass OR pc3.reltuples < 0 THEN (SELECT count(*) FROM "public"."users" c2) ELSE pc3.reltuples::bigint END FROM pg_catalog.pg_class pc3 WHERE pc3.oid = '"public"."users"'::regclass), $3 + 1) END FROM (SELECT (SELECT count(*) FROM (SELECT 1 FROM "public"."users" c6 LIMIT $3 + 1) b7) AS n) e8), 'n', count(*) FILTER (WHERE s1.__rn <= $2), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $2), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."users" t0
  ORDER BY t0."id" ASC
  LIMIT $1
) s1
-- args: [101,100,100]
