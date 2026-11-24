-- request: GET /api/kb/labels?select=id,~tags_icount,~nums_icount&tags=query_int.1%26(2%7C!3)&not.or=(tags.query_int.4,~tags_icount.gt.10)&order=~tags_icount.desc,id&limit=5
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $4), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $4), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", '~tags_icount', "ext".icount(t0."tags"), '~nums_icount', "ext".icount(t0."nums")) AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY ("ext".icount(t0."tags")) DESC NULLS LAST, t0."id" ASC) AS __rn
  FROM "kb"."labels" t0
  WHERE (t0."tags") OPERATOR("ext".@@) $1::"ext".query_int AND NOT (((t0."tags") OPERATOR("ext".@@) $2::"ext".query_int OR ("ext".icount(t0."tags")) > $3))
  ORDER BY ("ext".icount(t0."tags")) DESC NULLS LAST, t0."id" ASC
  LIMIT 6
) s1
-- args: ["1\u0026(2|!3)","4",10,5]
