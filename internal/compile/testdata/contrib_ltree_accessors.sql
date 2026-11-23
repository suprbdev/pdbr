-- request: GET /api/kb/docs?select=id,~path_nlevel,~path_parent,~paths_lca,~path_lca(path=a.b),~path_subpath(offset=1,length=2),tail:~path_subpath(offset=-1)&~path_nlevel=gte.2&order=~path_nlevel.desc,id&limit=5
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $8), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $8))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $8))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $8), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $8), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", '~path_nlevel', "ext".nlevel(t0."path"), '~path_parent', CASE WHEN "ext".nlevel(t0."path") > 0 THEN "ext".subpath(t0."path", 0, "ext".nlevel(t0."path") - 1) END, '~paths_lca', "ext".lca(array_remove(t0."paths", NULL)), '~path_lca', "ext".lca(t0."path", $1::"ext".ltree), '~path_subpath', CASE WHEN $2::int4 < "ext".nlevel(t0."path") AND -$2::int4 <= "ext".nlevel(t0."path") THEN "ext".subpath(t0."path", $2::int4, COALESCE($3::int4, 65535)) END, $6::text, CASE WHEN $4::int4 < "ext".nlevel(t0."path") AND -$4::int4 <= "ext".nlevel(t0."path") THEN "ext".subpath(t0."path", $4::int4, COALESCE($5::int4, 65535)) END) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY ("ext".nlevel(t0."path")) DESC NULLS LAST, t0."id" ASC) AS __rn
  FROM "kb"."docs" t0
  WHERE ("ext".nlevel(t0."path")) >= $7
  ORDER BY ("ext".nlevel(t0."path")) DESC NULLS LAST, t0."id" ASC
  LIMIT 6
) s1
-- args: ["a.b",1,2,-1,null,"tail",2,5]
