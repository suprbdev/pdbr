-- request: GET /api/users?select=settings->>theme,count(),total:settings->n::numeric.sum(),settings->>n.max(),c:settings->n.count()&order=settings->>theme.desc&count=true
SELECT json_build_object('has_next', coalesce(bool_or(s2.__rn > $7), false), 'has_prev', false, 'total', (SELECT count(*) FROM (SELECT 1 FROM "public"."users" c3 GROUP BY (c3."settings" ->> $8::text)) g4), 'n', count(*) FILTER (WHERE s2.__rn <= $7), 'rows', coalesce(json_agg(s2.data ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $7), '[]'::json)) AS data
FROM (
  SELECT json_build_object($2::text, g1."__k0", 'count', to_json(g1."__a0"), 'total', to_json(g1."__a1"), 'max', to_json(g1."__a2"), 'c', to_json(g1."__a3")) AS data, row_number() OVER (ORDER BY g1."__k0" DESC NULLS LAST) AS __rn
  FROM (
    SELECT (t0."settings" ->> $1::text) AS __k0, count(*) AS __a0, sum(((t0."settings" -> $3::text))::"pg_catalog"."numeric") AS __a1, max((t0."settings" ->> $4::text)) AS __a2, count((t0."settings" -> $5::text)) AS __a3
    FROM "public"."users" t0
    GROUP BY 1
  ) g1
  ORDER BY g1."__k0" DESC NULLS LAST
  LIMIT $6
) s2
-- args: ["theme","theme","n","n","n",101,100,"theme"]
