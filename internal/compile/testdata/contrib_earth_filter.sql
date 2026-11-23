-- request: GET /api/kb/sites?select=id,name,loc_earth_distance&loc=earth_lt.5000&latlon=51.5,-0.12
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $6), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $6), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $6), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'name', t0."name", 'loc_earth_distance', "ext".earth_distance((t0."loc"), "ext".ll_to_earth($1::float8, $2::float8))) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "kb"."sites" t0
  WHERE ("ext".earth_box("ext".ll_to_earth($3::float8, $4::float8), $5::float8) OPERATOR("ext".@>) (t0."loc") AND "ext".earth_distance((t0."loc"), "ext".ll_to_earth($3::float8, $4::float8)) < $5::float8)
  ORDER BY t0."id" ASC
  LIMIT 101
) s1
-- args: [51.5,-0.12,51.5,-0.12,5000,100]
