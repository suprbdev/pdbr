-- request: GET /api/places?select=id,name,users(id,places(id))&order=location.dist.asc,id&near=SRID%3D4326%3BPOINT(-0.1281%2051.508)&users.places.order=area.dist.desc&limit=3
SELECT jsonb_build_object('rows', coalesce(jsonb_agg(s6.data ORDER BY s6.__rn ASC) FILTER (WHERE s6.__rn <= $5), '[]'::jsonb), 'has_next', coalesce(bool_or(s6.__rn > $5), false), 'has_prev', false, 'first', (jsonb_agg(s6.__key ORDER BY s6.__rn ASC) FILTER (WHERE s6.__rn <= $5))->0, 'last', (jsonb_agg(s6.__key ORDER BY s6.__rn ASC) FILTER (WHERE s6.__rn <= $5))->-1) AS data
FROM (
  SELECT jsonb_build_object('id', t0."id", 'name', t0."name", 'users', l5.data) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."location" <-> ST_GeomFromEWKT($3) ASC, t0."id" ASC) AS __rn
  FROM "public"."places" t0
  LEFT JOIN LATERAL (
    SELECT jsonb_build_object('id', t1."id", 'places', l4.data) AS data
    FROM "public"."users" t1
    LEFT JOIN LATERAL (
      SELECT coalesce(jsonb_agg(s3.data), '[]'::jsonb) AS data
      FROM (
        SELECT jsonb_build_object('id', t2."id") AS data
        FROM "public"."places" t2
        WHERE t2."owner_id" = t1."id"
        ORDER BY t2."area" <-> (ST_GeomFromEWKT($1))::geography DESC NULLS LAST, t2."id" ASC
        LIMIT $2
      ) s3
    ) l4 ON true
    WHERE t1."id" = t0."owner_id"
    LIMIT 1
  ) l5 ON true
  ORDER BY t0."location" <-> ST_GeomFromEWKT($3) ASC, t0."id" ASC
  LIMIT $4
) s6
-- args: ["SRID=4326;POINT(-0.1281 51.508)",100,"SRID=4326;POINT(-0.1281 51.508)",4,3]
