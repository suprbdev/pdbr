-- request: GET /api/kb/sites?select=id&order=loc.earth_distance.desc&~latlon=51.5,-0.12&limit=2&after=WyJrYi5zaXRlcyIsIDNd
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $9), false), 'has_prev', true, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $9))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $9))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $9), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $9), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY (t0."loc") OPERATOR("ext".<->) "ext".ll_to_earth($1::float8, $2::float8) DESC NULLS LAST, t0."id" ASC) AS __rn
  FROM "kb"."sites" t0
  WHERE (((t0."loc") OPERATOR("ext".<->) "ext".ll_to_earth($7::float8, $8::float8) < (SELECT (__a."loc") OPERATOR("ext".<->) "ext".ll_to_earth($4::float8, $5::float8) FROM "kb"."sites" __a WHERE __a."id" = $3) OR ((t0."loc") OPERATOR("ext".<->) "ext".ll_to_earth($7::float8, $8::float8) IS NULL AND (SELECT (__a."loc") OPERATOR("ext".<->) "ext".ll_to_earth($4::float8, $5::float8) FROM "kb"."sites" __a WHERE __a."id" = $3) IS NOT NULL)) OR ((t0."loc") OPERATOR("ext".<->) "ext".ll_to_earth($7::float8, $8::float8) IS NOT DISTINCT FROM (SELECT (__a."loc") OPERATOR("ext".<->) "ext".ll_to_earth($4::float8, $5::float8) FROM "kb"."sites" __a WHERE __a."id" = $3) AND t0."id" > $6))
  ORDER BY (t0."loc") OPERATOR("ext".<->) "ext".ll_to_earth($1::float8, $2::float8) DESC NULLS LAST, t0."id" ASC
  LIMIT 3
) s1
-- args: [51.5,-0.12,3,51.5,-0.12,3,51.5,-0.12,2]
