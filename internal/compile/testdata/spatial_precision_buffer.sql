-- request: GET /api/places?select=id,location(precision=6),ring:area(buffer=100,precision=5),b:area(buffer=50,simplify=0.0001,transform=3857),c:area_centroid(buffer=10,precision=4),m2:area_area(buffer=10),n:location(buffer=-0.5)&order=id&limit=5
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $15), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $15))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $15))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $15), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $15), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'location', ST_AsGeoJSON(t0."location", $1::int4)::json, $4::text, ST_AsGeoJSON(ST_Buffer(t0."area", $2::float8), $3::int4)::json, $7::text, ST_AsGeoJSON(ST_Transform(ST_SimplifyPreserveTopology(ST_Buffer(t0."area", $5::float8)::geometry, $6), 3857))::json, $10::text, ST_AsGeoJSON(CASE WHEN ST_GeometryType(ST_Buffer(t0."area", $8::float8)::geometry) = 'ST_GeometryCollection' THEN ST_Centroid(ST_Buffer(t0."area", $8::float8)::geometry) ELSE ST_Centroid(ST_Buffer(t0."area", $8::float8))::geometry END, $9::int4)::json, $12::text, ST_Area(ST_Buffer(t0."area", $11::float8)), $14::text, ST_AsGeoJSON(ST_Buffer(t0."location", $13::float8))::json) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."places" t0
  ORDER BY t0."id" ASC
  LIMIT 6
) s1
-- args: [6,100,5,"ring",50,0.0001,"b",10,4,"c",10,"m2",-0.5,"n",5]
