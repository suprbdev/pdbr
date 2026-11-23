-- request: GET /api/rpc/sum_all?base=1&nums=2&nums=3
SELECT to_jsonb(__fn.v) AS data
FROM (SELECT "public"."sum_all"($1::"int4", VARIADIC $2::"int4"[]) AS v) AS __fn
-- args: [1,[2,3]]
