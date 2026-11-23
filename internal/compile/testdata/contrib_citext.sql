-- request: GET /api/kb/docs?select=id,email&email=eq.Ada@Example.com&email=neq.x&email=ilike.*ADA*&email=like.ada*&email=sw.Ad&email=not.ew.COM&email=in.(a@b.c,A@B.C)&email=isdistinct.bob@example.com&email=gte.a
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $10), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $10))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $10))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $10), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $10), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'email', t0."email") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "kb"."docs" t0
  WHERE t0."email" OPERATOR("ext".=) $1::"ext".citext AND NOT (t0."email" OPERATOR("ext".~~) ('%' || $2)::"ext".citext) AND t0."email" OPERATOR("ext".>=) $3::"ext".citext AND t0."email" OPERATOR("ext".~~*) $4::"ext".citext AND t0."email" OPERATOR("ext".=) ANY($5::text[]::"ext"."citext"[]) AND (t0."email" OPERATOR("ext".<>) $6::"ext".citext) IS NOT FALSE AND t0."email" OPERATOR("ext".~~) $7::"ext".citext AND t0."email" OPERATOR("ext".<>) $8::"ext".citext AND t0."email" OPERATOR("ext".~~) ($9 || '%')::"ext".citext
  ORDER BY t0."id" ASC
  LIMIT 101
) s1
-- args: ["Ada@Example.com","COM","a","%ADA%",["a@b.c","A@B.C"],"bob@example.com","ada%","x","Ad",100]
