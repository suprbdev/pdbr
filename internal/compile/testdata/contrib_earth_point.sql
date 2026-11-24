-- request: GET /api/kb/sites?select=id,~ll_earth_miles_distance&ll=not.earth_miles_distance_lt.10&order=ll.earth_miles_distance&~latlon=51.5,-0.12&limit=10
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $8), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $8))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $8))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $8), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $8), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", '~ll_earth_miles_distance', (t0."ll") OPERATOR("ext".<@>) point($2::float8, $1::float8)) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY (t0."ll") OPERATOR("ext".<@>) point($4::float8, $3::float8) ASC, t0."id" ASC) AS __rn
  FROM "kb"."sites" t0
  WHERE NOT (((t0."ll") OPERATOR("ext".<@>) point($6::float8, $5::float8)) < $7::float8)
  ORDER BY (t0."ll") OPERATOR("ext".<@>) point($4::float8, $3::float8) ASC, t0."id" ASC
  LIMIT 11
) s1
-- args: [51.5,-0.12,51.5,-0.12,51.5,-0.12,10,10]
