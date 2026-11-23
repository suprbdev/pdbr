-- request: GET /api/kb/docs?select=id,title&title=trgm_sim.postgres&or=(title.trgm_wsim.gres,title.not.trgm_swsim.post)&order=title.trgm_dist,title.trgm_wdist.desc&trgm=postgres&limit=5
SELECT json_build_object('rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $7), '[]'::json), 'has_next', coalesce(bool_or(s1.__rn > $7), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $7))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $7))->-1) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'title', t0."title") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY (t0."title") OPERATOR("ext".<->) $1::text ASC, (t0."title") OPERATOR("ext".<->>) $2::text DESC NULLS LAST, t0."id" ASC) AS __rn
  FROM "kb"."docs" t0
  WHERE (t0."title") OPERATOR("ext".%) $3::text AND (NOT ((t0."title") OPERATOR("ext".%>>) $4::text) OR (t0."title") OPERATOR("ext".%>) $5::text)
  ORDER BY (t0."title") OPERATOR("ext".<->) $1::text ASC, (t0."title") OPERATOR("ext".<->>) $2::text DESC NULLS LAST, t0."id" ASC
  LIMIT $6
) s1
-- args: ["postgres","postgres","postgres","post","gres",6,5]
