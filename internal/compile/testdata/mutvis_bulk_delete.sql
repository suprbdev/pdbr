-- request: DELETE /api/posts?title=like.M3*&select=id,users(posts(id))
WITH __mut AS (
  DELETE FROM "public"."posts" m0
  WHERE m0."title" LIKE $1
  RETURNING "id", "author_id"
),
__vis AS NOT MATERIALIZED (
  SELECT "id", "author_id" FROM "public"."posts" WHERE NOT EXISTS (SELECT 1 FROM __mut WHERE __mut."id" = "public"."posts"."id")
)
SELECT json_build_object('n', count(*), 'rows', coalesce(json_agg(s7.data), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', t1."id", 'users', l6.data) AS data
  FROM __mut t1
  LEFT JOIN LATERAL (
    SELECT json_build_object('posts', l5.data) AS data
    FROM "public"."users" t2
    LEFT JOIN LATERAL (
      SELECT coalesce(json_agg(s4.data), '[]'::json) AS data
      FROM (
        SELECT json_build_object('id', t3."id") AS data
        FROM __vis t3
        WHERE t3."author_id" = t2."id"
        ORDER BY t3."id" ASC
        LIMIT $2
      ) s4
    ) l5 ON true
    WHERE t2."id" = t1."author_id"
    LIMIT 1
  ) l6 ON true
) s7
-- args: ["M3%",100]
