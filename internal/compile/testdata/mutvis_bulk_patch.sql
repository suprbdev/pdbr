-- request: PATCH /api/posts?author_id=eq.1&select=id,users(posts(id,title))
WITH __mut AS (
  UPDATE "public"."posts" m0
  SET "title" = $2
  WHERE m0."author_id" = $1
  RETURNING "id", "author_id", "title"
),
__vis AS NOT MATERIALIZED (
  SELECT "id", "author_id", "title" FROM "public"."posts" WHERE NOT EXISTS (SELECT 1 FROM __mut WHERE __mut."id" = "public"."posts"."id")
  UNION ALL SELECT __mut."id", __mut."author_id", __mut."title" FROM __mut
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
        SELECT json_build_object('id', t3."id", 'title', t3."title") AS data
        FROM __vis t3
        WHERE t3."author_id" = t2."id"
        ORDER BY t3."id" ASC
        LIMIT $3
      ) s4
    ) l5 ON true
    WHERE t2."id" = t1."author_id"
    LIMIT 1
  ) l6 ON true
) s7
-- args: [1,"bulk-probe",100]
