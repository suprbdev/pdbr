-- request: GET /api/places?select=name,location,web_mercator:location(transform=3857),coarse:location(simplify=0.01),both:area(transform=3857,simplify=0.5),~location_srid,~location_geometry_type,sq_m:~area_area,~area_centroid,c3857:~area_centroid(transform=3857),~location_is_valid&order=id&limit=5
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $8), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $8))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $8))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $8), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $8), '[]'::json)) AS data
FROM (
  SELECT json_build_object('name', t0."name", 'location', ST_AsGeoJSON(t0."location")::json, $1::text, ST_AsGeoJSON(ST_Transform(t0."location", 3857))::json, $3::text, ST_AsGeoJSON(ST_SimplifyPreserveTopology(t0."location", $2))::json, $5::text, ST_AsGeoJSON(ST_Transform(ST_SimplifyPreserveTopology(t0."area"::geometry, $4), 3857))::json, '~location_srid', ST_SRID(t0."location"), '~location_geometry_type', ST_GeometryType(t0."location"), $6::text, ST_Area(t0."area"), '~area_centroid', ST_AsGeoJSON(CASE WHEN ST_GeometryType(t0."area"::geometry) = 'ST_GeometryCollection' THEN ST_Centroid(t0."area"::geometry) ELSE ST_Centroid(t0."area")::geometry END)::json, $7::text, ST_AsGeoJSON(ST_Centroid(ST_Transform(t0."area"::geometry, 3857)))::json, '~location_is_valid', ST_IsValid(t0."location")) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."places" t0
  ORDER BY t0."id" ASC
  LIMIT 6
) s1
-- args: ["web_mercator",0.01,"coarse",0.5,"both","sq_m","c3857",5]
