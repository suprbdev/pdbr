-- request: GET /api/places?select=id,name&location=st_intersects.SRID%3D4326%3BPOINT(-0.1281%2051.508)&area=not.st_within.%7B%22type%22%3A%22Polygon%22%2C%22coordinates%22%3A%5B%5B%5B-0.5%2C51.3%5D%2C%5B0.3%2C51.3%5D%2C%5B0.3%2C51.7%5D%2C%5B-0.5%2C51.7%5D%2C%5B-0.5%2C51.3%5D%5D%5D%7D&or=(location.bbox_intersects.(-0.5,51.3,0.3,51.7),location.bbox_containedby.(-55000,6700000,33000,6750000,3857),area.st_covers."SRID=4326;POINT(-0.128%2051.508)",location.is.null)&order=id
SELECT json_build_object('rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $13), '[]'::json), 'has_next', coalesce(bool_or(s1.__rn > $13), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $13))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $13))->-1) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'name', t0."name") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."places" t0
  WHERE NOT (ST_Within(t0."area"::geometry, CASE WHEN left($1, 1) = '{' THEN ST_GeomFromGeoJSON($1) ELSE ST_GeomFromEWKT($1) END)) AND ST_Intersects(t0."location", CASE WHEN left($2, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($2), 4326) ELSE ST_GeomFromEWKT($2) END) AND (ST_Covers(t0."area", (CASE WHEN left($3, 1) = '{' THEN ST_GeomFromGeoJSON($3) ELSE ST_GeomFromEWKT($3) END)::geography) OR t0."location" @ ST_MakeEnvelope($4, $5, $6, $7, 3857) OR t0."location" && ST_MakeEnvelope($8, $9, $10, $11, 4326) OR t0."location" IS NULL)
  ORDER BY t0."id" ASC
  LIMIT $12
) s1
-- args: ["{\"coordinates\":[[[-0.5,51.3],[0.3,51.3],[0.3,51.7],[-0.5,51.7],[-0.5,51.3]]],\"type\":\"Polygon\"}","SRID=4326;POINT(-0.1281 51.508)","SRID=4326;POINT(-0.128 51.508)",-55000,6700000,33000,6750000,-0.5,51.3,0.3,51.7,101,100]
