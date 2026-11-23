-- request: POST /api/posts?select=id,users(posts(id))
WITH __mut AS (
  INSERT INTO "public"."posts" ("author_id", "title")
  VALUES ($1, $2), ($3, $4)
  RETURNING "id", "author_id"
),
__vis AS NOT MATERIALIZED (
  SELECT "id", "author_id" FROM "public"."posts"
  UNION ALL SELECT __mut."id", __mut."author_id" FROM __mut
)
SELECT coalesce(json_agg(s6.data), '[]'::json) AS data
FROM (
  SELECT json_build_object('id', t0."id", 'users', l5.data) AS data
  FROM __mut t0
  LEFT JOIN LATERAL (
    SELECT json_build_object('posts', l4.data) AS data
    FROM "public"."users" t1
    LEFT JOIN LATERAL (
      SELECT coalesce(json_agg(s3.data), '[]'::json) AS data
      FROM (
        SELECT json_build_object('id', t2."id") AS data
        FROM __vis t2
        WHERE t2."author_id" = t1."id"
        ORDER BY t2."id" ASC
        LIMIT 100
      ) s3
    ) l4 ON true
    WHERE t1."id" = t0."author_id"
    LIMIT 1
  ) l5 ON true
) s6
-- args: [1,"One",2,"Two"]
