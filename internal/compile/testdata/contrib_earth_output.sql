-- request: GET /api/kb/sites?select=id,loc,ll
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $1), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $1))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $1))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $1), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $1), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'loc', (SELECT CASE WHEN __earth.e IS NULL THEN NULL ELSE json_build_object('lat', "ext".latitude(__earth.e), 'lon', "ext".longitude(__earth.e)) END FROM (SELECT t0."loc" AS e) AS __earth), 'll', t0."ll") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "kb"."sites" t0
  ORDER BY t0."id" ASC
  LIMIT 101
) s1
-- args: [100]
