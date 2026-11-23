-- request: GET /api/users?select=id&settings->>theme=eq.dark&settings->n=gt.20&settings->tags=cs.["a"]&settings->n=in.(1,2)&settings->>theme=not.in.(a,b)&settings->x=is.null&tags->>0=like.ad*
SELECT json_build_object('has_next', coalesce(bool_or(s1.__rn > $14), false), 'has_prev', false, 'first', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $14))->0, 'last', (json_agg(s1.__key ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $14))->-1, 'n', count(*) FILTER (WHERE s1.__rn <= $14), 'rows', coalesce(json_agg(s1.data ORDER BY s1.__rn ASC) FILTER (WHERE s1.__rn <= $14), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t0."id") AS data, jsonb_build_array(t0."id") AS __key, row_number() OVER (ORDER BY t0."id" ASC) AS __rn
  FROM "public"."users" t0
  WHERE (t0."settings" ->> $1::text) = $2 AND (t0."settings" ->> $3::text) <> ALL($4::"text"[]) AND (t0."settings" -> $5::text) @> $6::jsonb AND (t0."settings" -> $7::text) > $8 AND (t0."settings" -> $9::text) = ANY($10::"jsonb"[]) AND (t0."settings" -> $11::text) IS NULL AND (to_jsonb(t0."tags") ->> $12::int4) LIKE $13
  ORDER BY t0."id" ASC
  LIMIT 101
) s1
-- args: ["theme","dark","theme",["a","b"],"tags","[\"a\"]","n","20","n",["1","2"],"x",0,"ad%",100]
