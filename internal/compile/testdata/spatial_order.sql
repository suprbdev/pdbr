-- request: GET /api/places?select=id,name,users(id,places(id))&order=location.st_distance.asc,id&~near=SRID%3D4326%3BPOINT(-0.1281%2051.508)&users.places.order=area.st_distance.desc&limit=3
SELECT json_build_object('has_next', coalesce(bool_or(s6.__rn > $3), false), 'has_prev', false, 'first', (json_agg(s6.__key ORDER BY s6.__rn ASC) FILTER (WHERE s6.__rn <= $3))->0, 'last', (json_agg(s6.__key ORDER BY s6.__rn ASC) FILTER (WHERE s6.__rn <= $3))->-1, 'n', count(*) FILTER (WHERE s6.__rn <= $3), 'rows', coalesce(json_agg(s6.data ORDER BY s6.__rn ASC) FILTER (WHERE s6.__rn <= $3), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'name', t0."name", 'users', l5.data) AS data, jsonb_build_array(t0."id") AS __key, t0.__rn
  FROM (
    SELECT t0."id", t0."owner_id", t0."name", row_number() OVER (ORDER BY t0."location" <-> CASE WHEN left($2, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($2), 4326) ELSE ST_GeomFromEWKT($2) END ASC, t0."id" ASC) AS __rn
    FROM "public"."places" t0
    ORDER BY t0."location" <-> CASE WHEN left($2, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($2), 4326) ELSE ST_GeomFromEWKT($2) END ASC, t0."id" ASC
    LIMIT 4
  ) t0
  LEFT JOIN LATERAL (
    SELECT json_build_object('id', t1."id", 'places', l4.data) AS data
    FROM "public"."users" t1
    LEFT JOIN LATERAL (
      SELECT coalesce(json_agg(s3.data), '[]'::json) AS data
      FROM (
        SELECT json_build_object('id', t2."id") AS data
        FROM "public"."places" t2
        WHERE t2."owner_id" = t1."id"
        ORDER BY t2."area" <-> (CASE WHEN left($1, 1) = '{' THEN ST_GeomFromGeoJSON($1) ELSE ST_GeomFromEWKT($1) END)::geography DESC NULLS LAST, t2."id" ASC
        LIMIT 100
      ) s3
    ) l4 ON true
    WHERE t1."id" = t0."owner_id"
    LIMIT 1
  ) l5 ON true
) s6
-- args: ["SRID=4326;POINT(-0.1281 51.508)","SRID=4326;POINT(-0.1281 51.508)",3]
