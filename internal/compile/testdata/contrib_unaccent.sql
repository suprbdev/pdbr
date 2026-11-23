-- request: GET /api/kb/docs?select=id&body=unaccent_ilike.*cr%C3%A8me*br%C3%BBl%C3%A9e&and=(body.not.unaccent_eq.Caf%C3%A9)&or=(body.unaccent_includes.50%25,body.not.unaccent_sw.Zo%C3%AB_)&order=id
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $5), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $5))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $5))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $5), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $5), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "kb"."docs" t0
  WHERE NOT (lower("ext".unaccent('"ext"."unaccent"'::regdictionary, t0."body")) = lower("ext".unaccent('"ext"."unaccent"'::regdictionary, $1::text))) AND "ext".unaccent('"ext"."unaccent"'::regdictionary, t0."body") ILIKE "ext".unaccent('"ext"."unaccent"'::regdictionary, $2::text) AND ("ext".unaccent('"ext"."unaccent"'::regdictionary, t0."body") ILIKE "ext".unaccent('"ext"."unaccent"'::regdictionary, $3::text) OR NOT ("ext".unaccent('"ext"."unaccent"'::regdictionary, t0."body") ILIKE "ext".unaccent('"ext"."unaccent"'::regdictionary, $4::text)))
  ORDER BY t0."id" ASC
  LIMIT 101
) s1
-- args: ["Café","%crème%brûlée","%50\\%%","Zoë\\_%",100]
