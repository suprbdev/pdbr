-- request: GET /api/events?select=*,count()
SELECT json_build_object('rows', coalesce(json_agg(s2.data ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $2), '[]'::json), 'has_next', coalesce(bool_or(s2.__rn > $2), false), 'has_prev', false) AS data
FROM (
  SELECT json_build_object('id', g1."__k0", 'name', g1."__k1", 'type_code', g1."__k2", 'count', to_json(g1."__a0")) AS data, row_number() OVER (ORDER BY g1."__k0" ASC, g1."__k1" ASC, g1."__k2" ASC) AS __rn
  FROM (
    SELECT t0."id" AS __k0, t0."name" AS __k1, t0."type_code" AS __k2, count(*) AS __a0
    FROM "public"."events" t0
    GROUP BY 1, 2, 3
  ) g1
  ORDER BY g1."__k0" ASC, g1."__k1" ASC, g1."__k2" ASC
  LIMIT $1
) s2
-- args: [101,100]
