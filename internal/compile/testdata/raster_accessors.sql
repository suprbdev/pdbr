-- request: GET /api/kb/tiles?select=id,rast_width,rast_height,rast_num_bands,rast_srid,rast_pixel_width,rast_pixel_height,rast_envelope
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $1), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $1))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $1))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $1), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $1), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'rast_width', ST_Width(t0."rast"), 'rast_height', ST_Height(t0."rast"), 'rast_num_bands', ST_NumBands(t0."rast"), 'rast_srid', ST_SRID(t0."rast"), 'rast_pixel_width', ST_PixelWidth(t0."rast"), 'rast_pixel_height', ST_PixelHeight(t0."rast"), 'rast_envelope', ST_AsGeoJSON(ST_Envelope(t0."rast"))::json) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "kb"."tiles" t0
  ORDER BY t0."id" ASC
  LIMIT 101
) s1
-- args: [100]
