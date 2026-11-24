-- request: GET /api/users?select=id,places(name,~location_st_distance)&places.order=location.st_distance&places.limit=2&~near=POINT(0%200)&limit=2
SELECT json_build_object('has_next', coalesce(bool_or(s4.__rn > $3), false), 'has_prev', false, 'first', (json_agg(s4.__key ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $3))->0, 'last', (json_agg(s4.__key ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $3))->-1, 'n', count(*) FILTER (WHERE s4.__rn <= $3), 'rows', coalesce(json_agg(s4.data ORDER BY s4.__rn ASC) FILTER (WHERE s4.__rn <= $3), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'places', l3.data) AS data, jsonb_build_array(t0."id") AS __key, t0.__rn
  FROM (
    SELECT t0."id", row_number() OVER (ORDER BY t0."id" ASC) AS __rn
    FROM "public"."users" t0
    ORDER BY t0."id" ASC
    LIMIT 3
  ) t0
  LEFT JOIN LATERAL (
    SELECT coalesce(json_agg(s2.data), '[]'::json) AS data
    FROM (
      SELECT json_build_object('name', t1."name", '~location_st_distance', ST_Distance(t1."location", CASE WHEN left($1, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($1), 4326) ELSE ST_GeomFromEWKT($1) END)) AS data
      FROM "public"."places" t1
      WHERE t1."owner_id" = t0."id"
      ORDER BY t1."location" <-> CASE WHEN left($2, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($2), 4326) ELSE ST_GeomFromEWKT($2) END ASC, t1."id" ASC
      LIMIT 2
    ) s2
  ) l3 ON true
) s4
-- args: ["SRID=4326;POINT(0 0)","SRID=4326;POINT(0 0)",2]
