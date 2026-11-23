-- request: GET /api/places?select=owner_id,location.extent(),location.union(),location.collect(),location.centroid(),bbox:area.extent(),n:location.count()
SELECT json_build_object('has_next', coalesce(bool_or(s2.__rn > $4), false), 'has_prev', false, 'n', count(*) FILTER (WHERE s2.__rn <= $4), 'rows', coalesce(json_agg(s2.data ORDER BY s2.__rn ASC) FILTER (WHERE s2.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('owner_id', g1."__k0", 'extent', ST_AsGeoJSON(g1."__a0")::json, 'union', ST_AsGeoJSON(g1."__a1")::json, 'collect', ST_AsGeoJSON(g1."__a2")::json, 'centroid', ST_AsGeoJSON(g1."__a3")::json, $1::text, ST_AsGeoJSON(g1."__a4")::json, $2::text, to_json(g1."__a5")) AS data, row_number() OVER (ORDER BY g1."__k0" ASC) AS __rn
  FROM (
    SELECT t0."owner_id" AS __k0, ST_SetSRID(ST_Extent(t0."location")::geometry, 4326) AS __a0, ST_Union(t0."location") AS __a1, ST_Collect(t0."location") AS __a2, ST_Centroid(ST_Collect(t0."location")) AS __a3, ST_SetSRID(ST_Extent(t0."area"::geometry)::geometry, 4326) AS __a4, count(t0."location") AS __a5
    FROM "public"."places" t0
    GROUP BY 1
  ) g1
  ORDER BY g1."__k0" ASC
  LIMIT $3
) s2
-- args: ["bbox","n",101,100]
